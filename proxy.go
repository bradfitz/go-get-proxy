package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const modtimeFile = ".go-get-proxy-last"

var (
	listen = flag.String("listen", ":8080", "port, ip:port, or 'envfd:NAME' to listen on")
)

var (
	pendingMu sync.Mutex
	pending   = make(map[string]chan bool)
)

func proxy(w http.ResponseWriter, r *http.Request) {
	upath := r.URL.Path
	switch upath {
	case "/favicon.ico", "/robots.txt":
		// TODO(brafitz): handle
		return
	}
	if len(upath) < 2 {
		fmt.Fprintf(w, "<html><body>go get proxy</body></html>")
		return
	}
	if path.Clean(upath) != upath {
		log.Printf("invalid requested path %q", upath)
		http.Error(w, "invalid path", 500)
		return
	}
	pkg := upath[1:]

	path, err := getPackage(pkg)
	if err != nil {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(500)
		fmt.Fprintf(w, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/x-tar")
	err = makeTar(w, path)
	if err != nil {
		log.Printf("Error generating tar of %q: %v", path, err)
	}
}

var goPathSrc = filepath.Join(os.Getenv("GOPATH"), "src")

const newEnough = 1 * time.Minute

func isNewEnough(dir string) (ret bool) {
	topDir := dir
	defer func() {
		log.Printf("newEnough(%q) ... %q = %v", topDir, dir, ret)
	}()
	for len(dir) > len(goPathSrc) {
		log.Printf("is %q new enough?", dir)
		if fi, err := os.Stat(filepath.Join(dir, modtimeFile)); err == nil {
			if time.Now().Sub(fi.ModTime()) < newEnough {
				return true
			}
		}
		dir = filepath.Join(dir, "..")
	}
	return false
}

func getPackage(pkg string) (pkgPath string, err error) {
	root := filepath.Join(goPathSrc, filepath.FromSlash(pkg))
	if isNewEnough(root) {
		return root, nil
	}

	pendingMu.Lock()
	c, ok := pending[pkg]
	if !ok {
		c = make(chan bool, 1)
		pending[pkg] = c
	}
	pendingMu.Unlock()
	c <- true // blocks until buffer size of 1 is free
	defer func() { <-c }()

	// TODO(bradfitz): this isn't perfect synchronization. we're
	// only protecting the top level. the go get tool will go
	// fetch dependencies that we don't see here.

	log.Printf("Getting package %q...", pkg)
	out, err := exec.Command("go", "get", "-u", pkg).CombinedOutput()
	touchFile(filepath.Join(root, modtimeFile))
	if err != nil {
		log.Printf("Get of package %q failed: %v; output: %s", pkg, err, out)
		return "", fmt.Errorf("Error running go get for package %q: %v\n\nOutput:\n%s", pkg, err, out)
	}
	log.Printf("Fetched package %q", pkg)
	return root, nil
}

func touchFile(name string) {
	os.Remove(name)
	f, err := os.Create(name)
	if err == nil {
		f.Close()
	}
}

func main() {
	flag.Parse()

	var ln net.Listener
	addr := *listen
	if strings.HasPrefix(addr, "envfd:") {
		name := addr[len("envfd:"):]
		fdstr := os.Getenv("RUNSIT_PORTFD_" + name)
		if fdstr == "" {
			log.Fatalf("didn't find named runsit port named %q in environment", name)
		}
		fdnum, err := strconv.Atoi(fdstr)
		if err != nil {
			log.Fatalf("bogus port number %q in environment: %v", fdstr, err)
		}
		ln, err = net.FileListener(os.NewFile(uintptr(fdnum), "fd"))
		if err != nil {
			log.Fatal(err)
		}
	} else {
		if !strings.Contains(addr, ":") {
			addr = ":" + addr
		}
		var err error
		ln, err = net.Listen("tcp", addr)
		if err != nil {
			log.Fatalf("Listen on %q: %v", addr, err)
		}
	}
	s := &http.Server{
		Handler: http.HandlerFunc(proxy),
	}
	log.Printf("Listened on %q; starting.", addr)
	err := s.Serve(ln)
	log.Fatalf("Serve error: %v", err)
}
