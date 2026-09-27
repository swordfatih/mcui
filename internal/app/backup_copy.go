package app

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// copyBackupTree copies only persistent data selected by the shared policy.
func copyBackupTree(src, dst, edition string) error {
	plan, err := planData(src, edition)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0700); err != nil {
		return err
	}
	root, err := os.OpenRoot(src)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, item := range plan.Keep {
		if err := copyPreservedFromRoot(root, item.Path, filepath.Join(dst, filepath.FromSlash(item.Path))); err != nil {
			return err
		}
	}
	return nil
}

func copyPreservedFromRoot(root *os.Root, rel, dst string) error {
	if !assetRelative(rel) {
		return fs.ErrInvalid
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
		return err
	}
	in, err := root.Open(rel)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fs.ErrInvalid
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Chtimes(dst, info.ModTime(), info.ModTime())
}
