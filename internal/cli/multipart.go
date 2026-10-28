package cli

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"os"
	"path/filepath"
	"strings"

	"github.com/ehmo/gomoufox"
	"github.com/ehmo/gomoufox/internal/policy"
)

var newMultipartWriter = multipart.NewWriter
var closeMultipartInput = (*os.File).Close

type boundedFormBuffer struct{ bytes.Buffer }

func (b *boundedFormBuffer) Write(p []byte) (int, error) {
	if len(p) > gomoufox.MaxTransferFileBytes-b.Len() {
		return 0, fmt.Errorf("multipart body exceeds %d bytes", gomoufox.MaxTransferFileBytes)
	}
	return b.Buffer.Write(p)
}

func splitFormPair(value string) (string, string, error) {
	name, value, ok := strings.Cut(value, "=")
	if !ok || name == "" || len(name) > 256 || strings.ContainsAny(name, "\r\n\x00") {
		return "", "", fmt.Errorf("multipart values require name=value; names must be 1..256 bytes without control separators")
	}
	return name, value, nil
}

func multipartBody(req LocalCommandRequest) ([]byte, string, error) {
	var buf boundedFormBuffer
	writer := newMultipartWriter(&buf)
	fields, files := flagStringList(req, "form"), flagStringList(req, "form_file")
	if len(fields) > 128 || len(files) > 32 {
		return nil, "", fmt.Errorf("multipart accepts at most 128 fields and 32 files")
	}
	fieldBytes := 0
	for _, raw := range fields {
		name, value, err := splitFormPair(raw)
		if err != nil {
			return nil, "", err
		}
		fieldBytes += len(name) + len(value)
		if fieldBytes > policy.HardMaxInputBytes {
			return nil, "", fmt.Errorf("multipart text exceeds %d bytes; use --form-file for file data", policy.HardMaxInputBytes)
		}
		if err := writer.WriteField(name, value); err != nil {
			return nil, "", err
		}
	}
	for _, raw := range files {
		name, path, err := splitFormPair(raw)
		if err != nil {
			return nil, "", err
		}
		if path == "" || strings.ContainsAny(filepath.Base(path), "\r\n\x00") {
			return nil, "", fmt.Errorf("--form-file requires name=path with a valid filename")
		}
		file, err := openInputFile(req, path)
		if err != nil {
			return nil, "", err
		}
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() {
			_ = file.Close()
			if err != nil {
				return nil, "", err
			}
			return nil, "", fmt.Errorf("multipart input must be a regular file")
		}
		part, err := writer.CreateFormFile(name, filepath.Base(path))
		if err == nil {
			_, err = io.Copy(part, io.LimitReader(file, int64(gomoufox.MaxTransferFileBytes-buf.Len())+1))
		}
		closeErr := closeMultipartInput(file)
		if err != nil {
			return nil, "", err
		}
		if closeErr != nil {
			return nil, "", closeErr
		}
	}
	if err := writer.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), writer.FormDataContentType(), nil
}
