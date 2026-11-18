package proxyconnect

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/term-llm/term-llm/internal/filelock"
	"github.com/term-llm/term-llm/internal/fileprivacy"
)

// Options selects the legacy Claude settings to undo and the status output.
// Home defaults to the current user's home; Out defaults to standard error.
type Options struct {
	Home string
	Out  io.Writer
}

// Disconnect undoes a legacy Claude settings enrollment using its private
// receipt. It is local-only: server clients must be revoked manually in Clients.
// A missing receipt is a successful no-op; unowned files and credentials remain.
func Disconnect(opts Options) error {
	if opts.Home == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("locate user home: %w", err)
		}
		opts.Home = home
	}
	if opts.Out == nil {
		opts.Out = os.Stderr
	}
	dir, err := claudeSettingsDir(opts.Home)
	if err != nil {
		return err
	}
	// Do not create directories or lock files on an installation with no receipt.
	if _, recorded, err := readPrivateRegular(filepath.Join(dir, claudeReceiptName), "connection record"); err != nil {
		return err
	} else if !recorded {
		fmt.Fprintln(opts.Out, "No legacy Claude settings receipt; nothing to undo. Revoke unused clients manually in Clients.")
		return nil
	}
	_, unlock, err := connectorLock(opts.Home)
	if err != nil {
		return err
	}
	defer unlock()
	if err := undoClaudeSettings(opts.Home); err != nil {
		return err
	}
	// Enrollment metadata and native trust files are not settings undo evidence;
	// leave them alone rather than guessing ownership from a filename.
	fmt.Fprintln(opts.Out, "Legacy Claude settings restored. Other settings and native credentials unchanged.\nRevoke the unused server client manually in Clients.")
	return nil
}

func connectorLock(home string) (string, func() error, error) {
	dir, err := claudeSettingsDir(home)
	if err != nil {
		return "", nil, err
	}
	lock := filepath.Join(dir, ".term-llm-connect.lock")
	if info, e := os.Lstat(lock); e == nil {
		if !info.Mode().IsRegular() || !ownedByCurrentUser(info) || fileprivacy.CheckFile(info) != nil {
			return "", nil, errors.New("Unsafe connector lock file")
		}
	} else if !os.IsNotExist(e) {
		return "", nil, errors.New("Could not inspect connector lock")
	}
	unlock, err := filelock.TryLock(lock)
	if err != nil {
		return "", nil, errors.New("Another Claude connector operation is running")
	}
	return dir, unlock, nil
}
