package app

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

type centralUI interface {
	resume() error
	suspend() error
	size() (int, int)
	key() (string, error)
}

// Input is polled only while Central owns the terminal. No background reader
// can steal keystrokes from nvim, lazygit, or the Docker/tmux attachment.
type centralTerminal struct {
	in    *os.File
	out   io.Writer
	outFD int
	state *term.State
}

func newCentralTerminal(in io.Reader, out io.Writer) (*centralTerminal, error) {
	file, ok := in.(*os.File)
	fd, outOK := out.(interface{ Fd() uintptr })
	if !ok || !outOK || !term.IsTerminal(int(file.Fd())) || !term.IsTerminal(int(fd.Fd())) {
		return nil, fmt.Errorf("central requires an interactive terminal")
	}
	state, err := term.GetState(int(file.Fd()))
	if err != nil {
		return nil, err
	}
	return &centralTerminal{in: file, out: out, outFD: int(fd.Fd()), state: state}, nil
}

func (t *centralTerminal) resume() error {
	if err := term.Restore(int(t.in.Fd()), t.state); err != nil {
		return err
	}
	if _, err := term.MakeRaw(int(t.in.Fd())); err != nil {
		return err
	}
	_, err := io.WriteString(t.out, "\x1b[?1049h\x1b[?25l\x1b[2J\x1b[H")
	return err
}

func (t *centralTerminal) suspend() error {
	_, writeErr := io.WriteString(t.out, "\x1b[?25h\x1b[?1049l")
	return errors.Join(writeErr, term.Restore(int(t.in.Fd()), t.state))
}

func (t *centralTerminal) size() (int, int) {
	width, height, err := term.GetSize(t.outFD)
	if err != nil || width < 1 || height < 1 {
		return 80, 24
	}
	return width, height
}

func (t *centralTerminal) readByte(timeout int) (byte, bool, error) {
	fds := []unix.PollFd{{Fd: int32(t.in.Fd()), Events: unix.POLLIN}}
	n, err := unix.Poll(fds, timeout)
	if errors.Is(err, unix.EINTR) {
		return 0, false, nil
	}
	if err != nil || n == 0 {
		return 0, false, err
	}
	if fds[0].Revents&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 {
		return 0, false, io.EOF
	}
	var buffer [1]byte
	n, err = t.in.Read(buffer[:])
	if err != nil {
		return 0, false, err
	}
	if n == 0 {
		return 0, false, io.EOF
	}
	return buffer[0], true, nil
}

func (t *centralTerminal) key() (string, error) {
	b, ok, err := t.readByte(100)
	if err != nil || !ok {
		return "", err
	}
	if b == 27 {
		next, ok, err := t.readByte(30)
		if err != nil || !ok {
			return "\x1b", err
		}
		if next == '[' || next == 'O' {
			for i := 0; i < 16; i++ {
				last, ok, err := t.readByte(30)
				if err != nil || !ok {
					return "", err
				}
				if last >= 0x40 && last <= 0x7e {
					switch last {
					case 'A':
						return "up", nil
					case 'B':
						return "down", nil
					}
					return "", nil
				}
			}
		}
		return "", nil
	}
	bytes := []byte{b}
	for !utf8.FullRune(bytes) && len(bytes) < utf8.UTFMax {
		next, ok, err := t.readByte(30)
		if err != nil || !ok {
			return "", err
		}
		bytes = append(bytes, next)
	}
	if !utf8.Valid(bytes) {
		return "", nil
	}
	return string(bytes), nil
}

// Startup output never touches the live TUI or grows without bound.
type centralLog struct {
	mu   sync.Mutex
	text string
}

func (l *centralLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.text += string(p)
	const limit = 16 * 1024
	if len(l.text) > limit {
		l.text = l.text[len(l.text)-limit:]
	}
	return len(p), nil
}

func (l *centralLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.text
}

// Dashboard decorations are monochrome, single-cell terminal glyphs. Keep
// arbitrary non-Latin scripts and emoji conservative to avoid wrapping rows.
func centralRuneCells(r rune) int {
	if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) {
		return 0
	}
	if r < 0x1100 || strings.ContainsRune("─✦●○×◐◓◑◒◇↳▏·–", r) {
		return 1
	}
	return 2
}

func centralText(text string, width int) string {
	var result strings.Builder
	cells := 0
	for _, r := range strings.ToValidUTF8(text, "?") {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			r = ' '
		}
		size := centralRuneCells(r)
		if cells+size > width {
			break
		}
		result.WriteRune(r)
		cells += size
	}
	return result.String()
}
