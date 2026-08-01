package logging

import (
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	gatewayArchiveCompressionDelay = 30 * time.Second
	gatewayArchiveScanInterval     = time.Second
)

// gatewayArchiveCompressor leaves a renamed log file readable long enough for
// Alloy to drain its old inode before atomically replacing the archive with gzip.
type gatewayArchiveCompressor struct {
	activePath string
	delay      time.Duration
	notBefore  time.Time
	stop       chan struct{}
	done       chan struct{}
	startOnce  sync.Once
	closeOnce  sync.Once
}

func newGatewayArchiveCompressor(activePath string, delay time.Duration) *gatewayArchiveCompressor {
	return &gatewayArchiveCompressor{
		activePath: activePath,
		delay:      delay,
		notBefore:  time.Now().Add(delay),
		stop:       make(chan struct{}),
		done:       make(chan struct{}),
	}
}

func (c *gatewayArchiveCompressor) Start() {
	if c == nil {
		return
	}
	c.startOnce.Do(func() { go c.run() })
}

func (c *gatewayArchiveCompressor) Close() error {
	if c == nil {
		return nil
	}
	c.Start()
	c.closeOnce.Do(func() { close(c.stop) })
	<-c.done
	return c.compressReady(time.Now())
}

func (c *gatewayArchiveCompressor) run() {
	defer close(c.done)
	ticker := time.NewTicker(gatewayArchiveScanInterval)
	defer ticker.Stop()
	for {
		select {
		case now := <-ticker.C:
			if err := c.compressReady(now); err != nil {
				_, _ = fmt.Fprintf(os.Stderr, "gateway log archive compression failed: %v\n", err)
			}
		case <-c.stop:
			return
		}
	}
}

func (c *gatewayArchiveCompressor) compressReady(now time.Time) error {
	if c == nil || now.Before(c.notBefore) {
		return nil
	}
	archives, err := gatewayUncompressedArchives(c.activePath)
	if err != nil {
		return err
	}
	var result error
	for _, archive := range archives {
		info, statErr := os.Stat(archive)
		if statErr != nil {
			if !os.IsNotExist(statErr) {
				result = errors.Join(result, statErr)
			}
			continue
		}
		if now.Sub(info.ModTime()) < c.delay {
			continue
		}
		if compressErr := gzipGatewayArchive(archive, info.Mode()); compressErr != nil && !os.IsNotExist(compressErr) {
			result = errors.Join(result, compressErr)
		}
	}
	return result
}

func gatewayUncompressedArchives(activePath string) ([]string, error) {
	base := strings.TrimSuffix(filepath.Base(activePath), filepath.Ext(activePath))
	extension := filepath.Ext(activePath)
	return filepath.Glob(filepath.Join(filepath.Dir(activePath), base+"-*"+extension))
}

func gzipGatewayArchive(source string, mode os.FileMode) error {
	destination := source + ".gz"
	temporary := destination + ".tmp"
	if _, err := os.Stat(destination); err == nil {
		return os.Remove(source)
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Remove(temporary); err != nil && !os.IsNotExist(err) {
		return err
	}

	input, err := os.Open(source)
	if err != nil {
		return err
	}

	output, err := os.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode.Perm())
	if err != nil {
		return errors.Join(err, input.Close())
	}

	writer := gzip.NewWriter(output)
	_, copyErr := io.Copy(writer, input)
	closeWriterErr := writer.Close()
	syncErr := output.Sync()
	closeOutputErr := output.Close()
	closeInputErr := input.Close()
	if err := errors.Join(copyErr, closeWriterErr, syncErr, closeOutputErr, closeInputErr); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	if err := os.Rename(temporary, destination); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return os.Remove(source)
}
