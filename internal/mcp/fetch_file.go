package mcp

import (
	"io"
	"os"
)

func (s *Server) readFetchBodyFile(path string) ([]byte, string) {
	resolved, code := s.prepareUploadPath(path)
	if code != "" {
		return nil, code
	}
	rel, err := mcpConfinedPath(s.jail, resolved)
	if err != nil {
		return nil, "path_rejected"
	}
	root, err := os.OpenRoot(s.jail.Root)
	if err != nil {
		return nil, "path_rejected"
	}
	defer root.Close()
	file, err := root.Open(rel)
	if err != nil {
		return nil, "path_rejected"
	}
	defer file.Close()
	return readFetchBody(file)
}

func readFetchBody(file interface {
	io.Reader
	Stat() (os.FileInfo, error)
}) ([]byte, string) {
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, "path_rejected"
	}
	if info.Size() > maxUploadFileBytes {
		return nil, "file_too_large"
	}
	data, err := io.ReadAll(io.LimitReader(file, maxUploadFileBytes+1))
	if err != nil {
		return nil, "file_read_failed"
	}
	if int64(len(data)) > maxUploadFileBytes {
		return nil, "file_too_large"
	}
	return data, ""
}
