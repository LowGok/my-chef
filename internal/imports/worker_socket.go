package imports

import (
	"net"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/net/netutil"
)

type workerPeerListener struct {
	net.Listener
	appUID uint32
}

func (l workerPeerListener) Accept() (net.Conn, error) {
	for {
		connection, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		unix, ok := connection.(*net.UnixConn)
		if !ok {
			_ = connection.Close()
			continue
		}
		raw, err := unix.SyscallConn()
		if err != nil {
			_ = connection.Close()
			continue
		}
		var peer *syscall.Ucred
		var peerErr error
		err = raw.Control(func(fd uintptr) {
			peer, peerErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
		})
		if err != nil || peerErr != nil || peer == nil || peer.Uid != l.appUID {
			_ = connection.Close()
			continue
		}
		return connection, nil
	}
}

// ListenPrivateWorker exposes no TCP listener. The kernel peer identity and
// per-request channel key both gate access; the app receives no admin API.
func ListenPrivateWorker(socket string, appUID uint32) (net.Listener, error) {
	if !filepath.IsAbs(socket) || filepath.Base(socket) != "import-worker.sock" {
		return nil, ErrExtractionUnavailable
	}
	directory, err := os.Lstat(filepath.Dir(socket))
	if err != nil || !directory.IsDir() || directory.Mode().Perm()&0002 != 0 || (directory.Mode().Perm()&0020 != 0 && directory.Mode()&os.ModeSticky == 0) {
		return nil, ErrExtractionUnavailable
	}
	stat, ok := directory.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return nil, ErrExtractionUnavailable
	}
	if info, err := os.Lstat(socket); err == nil {
		owner, ok := info.Sys().(*syscall.Stat_t)
		if info.Mode()&os.ModeSocket == 0 || !ok || owner.Uid != uint32(os.Geteuid()) {
			return nil, ErrExtractionUnavailable
		}
		if err := os.Remove(socket); err != nil {
			return nil, ErrExtractionUnavailable
		}
	} else if !os.IsNotExist(err) {
		return nil, ErrExtractionUnavailable
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		return nil, ErrExtractionUnavailable
	}
	if err := os.Chmod(socket, 0660); err != nil {
		_ = listener.Close()
		return nil, ErrExtractionUnavailable
	}
	return netutil.LimitListener(workerPeerListener{Listener: listener, appUID: appUID}, 4), nil
}
