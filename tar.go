// tar writing, largely copied from go/misc/bindist.go

package main

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// sysStat, if non-nil, populates h from system-dependent fields of fi.
var sysStat func(fi os.FileInfo, h *tar.Header) error

// Mode constants from the tar spec.
const (
	c_ISDIR  = 040000
	c_ISFIFO = 010000
	c_ISREG  = 0100000
	c_ISLNK  = 0120000
	c_ISBLK  = 060000
	c_ISCHR  = 020000
	c_ISSOCK = 0140000
)

// tarFileInfoHeader creates a partially-populated Header from an os.FileInfo.
// The filename parameter is used only in the case of symlinks, to call os.Readlink.
// If fi is a symlink but filename is empty, an error is returned.
func tarFileInfoHeader(fi os.FileInfo, filename string) (*tar.Header, error) {
	h := &tar.Header{
		Name:    fi.Name(),
		ModTime: fi.ModTime(),
		Mode:    int64(fi.Mode().Perm()), // or'd with c_IS* constants later
	}
	switch {
	case fi.Mode()&os.ModeType == 0:
		h.Mode |= c_ISREG
		h.Typeflag = tar.TypeReg
		h.Size = fi.Size()
	case fi.IsDir():
		h.Typeflag = tar.TypeDir
		h.Mode |= c_ISDIR
	case fi.Mode()&os.ModeSymlink != 0:
		h.Typeflag = tar.TypeSymlink
		h.Mode |= c_ISLNK
		if filename == "" {
			return h, fmt.Errorf("archive/tar: unable to populate Header.Linkname of symlinks")
		}
		targ, err := os.Readlink(filename)
		if err != nil {
			return h, err
		}
		h.Linkname = targ
	case fi.Mode()&os.ModeDevice != 0:
		if fi.Mode()&os.ModeCharDevice != 0 {
			h.Mode |= c_ISCHR
			h.Typeflag = tar.TypeChar
		} else {
			h.Mode |= c_ISBLK
			h.Typeflag = tar.TypeBlock
		}
	case fi.Mode()&os.ModeSocket != 0:
		h.Mode |= c_ISSOCK
	default:
		return nil, fmt.Errorf("archive/tar: unknown file mode %v", fi.Mode())
	}
	if sysStat != nil {
		return h, sysStat(fi, h)
	}
	return h, nil
}

func makeTar(w io.Writer, workdir string) error {
	zout := gzip.NewWriter(w)
	tw := tar.NewWriter(zout)

	err := filepath.Walk(workdir, filepath.WalkFunc(func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			log.Printf("Error walking path %q: %v", path, err)
		}
		if fi == nil {
			log.Printf("Odd: nil os.Fileinfo for path %q", path)
			return nil
		}
		if !strings.HasPrefix(path, workdir) {
			log.Panicf("walked filename %q doesn't begin with workdir %q", path, workdir)
		}
		name := path[len(workdir):]

		// Chop of any leading / from filename, leftover from removing workdir.
		if strings.HasPrefix(name, "/") {
			name = name[1:]
		}
		if name == modtimeFile {
			return nil
		}

		if fi.IsDir() {
			if name != "" {
				// Just return the top-level files in the directory.
				return filepath.SkipDir
			}
			return nil
		}

		if !strings.HasSuffix(name, ".go") && fi.Size() > 10<<10 {
			// Skip non-go files over some threshold
			return nil
		}
		if fi.Size() > 1<<20 {
			// Skip all files over some other threshold.
			return nil
		}

		hdr, err := tarFileInfoHeader(fi, path)
		if err != nil {
			log.Printf("error making header of %q: %v", path, err)
			return err
		}
		hdr.Name = name
		hdr.Uname = "root"
		hdr.Gname = "root"
		hdr.Uid = 0
		hdr.Gid = 0

		// Force permissions to 0755 for executables, 0644 for everything else.
		if fi.Mode().Perm()&0111 != 0 {
			hdr.Mode = hdr.Mode&^0777 | 0755
		} else {
			hdr.Mode = hdr.Mode&^0777 | 0644
		}

		err = tw.WriteHeader(hdr)
		if err != nil {
			log.Printf("WriteHeader: %v", err)
			return fmt.Errorf("Error writing file %q: %v", name, err)
		}
		r, err := os.Open(path)
		if err != nil {
			log.Printf("Open: %v", err)
			return err
		}
		defer r.Close()
		_, err = io.Copy(tw, r)
		return err
	}))
	if err != nil {
		return err
	}

	if err := tw.Close(); err != nil {
		return err
	}
	if err := zout.Close(); err != nil {
		return err
	}
	return nil
}
