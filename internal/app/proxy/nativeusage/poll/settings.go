package poll

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/term-llm/term-llm/internal/fileprivacy"
)

const (
	DefaultMinutes = 15
	MinMinutes     = 10
	MaxMinutes     = 1440
)

var ErrInterval = fmt.Errorf("Interval must be a whole number from %d to %d minutes", MinMinutes, MaxMinutes)

// Open always returns a usable cache. An invalid or unreadable preferences
// file is reported to the caller, and the cache uses the default interval.
func Open(dir string) (*Cache, error) {
	c := &Cache{path: filepath.Join(dir, "native-usage-interval"), interval: DefaultMinutes, wake: make(chan struct{}, 1)}
	minutes, err := loadMinutes(c.path)
	if err == nil {
		c.interval = minutes
	}
	if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	return c, err
}

func loadMinutes(path string) (int, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return 0, err
	}
	if !info.Mode().IsRegular() || fileprivacy.CheckFile(info) != nil {
		return 0, errors.New("usage settings must be a private regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 32))
	if err != nil {
		return 0, err
	}
	minutes, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if len(data) == 32 || err != nil || !validMinutes(minutes) {
		return 0, ErrInterval
	}
	return minutes, nil
}

func validMinutes(minutes int) bool { return minutes >= MinMinutes && minutes <= MaxMinutes }

func (c *Cache) SetInterval(minutes int) error {
	if !validMinutes(minutes) {
		return ErrInterval
	}
	c.settingsMu.Lock()
	defer c.settingsMu.Unlock()
	c.mu.Lock()
	stopping := c.stopping()
	c.mu.Unlock()
	if stopping {
		return ErrClosed
	}
	if err := fileprivacy.WriteFileAtomic(c.path, []byte(strconv.Itoa(minutes)+"\n")); err != nil {
		return fmt.Errorf("save usage interval: %w", err)
	}
	c.setInterval(minutes)
	return nil
}
