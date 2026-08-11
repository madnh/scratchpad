// Package ptyrun runs a child program on a pseudo-terminal and lets a second writer type
// into it.
//
// It exists because there is no way to put a line into another process's input from the
// outside. Knowing a pid answers only whether something is alive (`kill -0`); writing to
// its terminal from a stranger is `TIOCSTI`, which modern Linux disables and macOS does
// not offer safely. The one arrangement that can both KNOW the child is alive and TYPE
// into it is being its parent and owning its terminal — which is all this package is.
//
// What it buys, beyond the write: certainty. A wrapper that owns the pty learns the child
// exited from `wait`, not by reading the screen and guessing whether that prompt belongs
// to an agent or to the shell that replaced it. Guessing there is how a message meant for
// an agent becomes a command executed by a shell.
//
// The cost is honest and worth stating: this code sits between the operator and every
// keystroke they type. Raw mode, window resizes and cleanup on exit all have to be right,
// and when they are wrong they are wrong in the irritating way (a terminal left in raw
// mode) rather than the loud way. That is why nothing here is on by default — it runs
// only when someone explicitly launches their agent through `scratchpad exec`.
package ptyrun

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"
)

// Session is a child process running on a pty this process owns.
type Session struct {
	cmd  *exec.Cmd
	ptmx *os.File

	// writeMu serialises the two writers into the child's input: the operator's keystrokes
	// being copied from stdin, and Type. Without it a nudge can be spliced into the middle
	// of a line someone is halfway through typing.
	writeMu sync.Mutex

	restore func()
	stopWin chan struct{}
}

// Start launches argv on a fresh pty, wires the current terminal to it, and returns as
// soon as the child is running. env replaces the child's environment entirely (pass
// os.Environ() plus your own additions).
func Start(argv []string, env []string) (*Session, error) {
	if len(argv) == 0 {
		return nil, errors.New("nothing to run")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = env

	ptmx, err := pty.Start(cmd)
	if err != nil {
		return nil, err
	}
	s := &Session{cmd: cmd, ptmx: ptmx, stopWin: make(chan struct{})}

	// Match the pty to the real terminal now and on every resize, so the child's UI is
	// laid out for the window the operator is actually looking at.
	if term.IsTerminal(int(os.Stdin.Fd())) {
		win := make(chan os.Signal, 1)
		signal.Notify(win, syscall.SIGWINCH)
		go func() {
			defer signal.Stop(win)
			for {
				select {
				case <-win:
					_ = pty.InheritSize(os.Stdin, ptmx)
				case <-s.stopWin:
					return
				}
			}
		}()
		win <- syscall.SIGWINCH // set the initial size

		// Raw mode: the child's own UI does the echoing and line editing, so this process
		// must hand every byte through untouched. The restore is registered immediately —
		// a terminal left raw is the failure mode that outlives the program.
		if state, err := term.MakeRaw(int(os.Stdin.Fd())); err == nil {
			s.restore = func() { _ = term.Restore(int(os.Stdin.Fd()), state) }
		}
	}

	go func() {
		// Keystrokes in. A read error means stdin closed; the child keeps running and
		// Wait still reports its exit honestly.
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				s.writeMu.Lock()
				_, werr := ptmx.Write(buf[:n])
				s.writeMu.Unlock()
				if werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	return s, nil
}

// submitDelay separates the text from the Enter that submits it.
//
// It is not politeness, it is the difference between working and not. A TUI that sees a
// burst of bytes arrive together treats it as a PASTE, and Enter inside a paste is a
// newline, not "send" — deliberately, so pasting three lines does not fire three times.
// Measured with Codex: text plus `\r` in one write left the line sitting in the composer
// indefinitely; the same `\r` sent on its own submitted immediately.
const submitDelay = 150 * time.Millisecond

// Type puts a line into the child's input exactly as if it had been typed, then submits it.
//
// It is deliberately NOT wrapped in bracketed-paste markers. Those only help if the child
// has turned that mode on, and the bytes are inert nonsense to anything that has not —
// which is the one case where being careful matters. What this sends is a plain line, so
// it means the same thing to every reader.
//
// The lock is held across the pause so the operator's own keystrokes cannot land between
// the text and its Enter — that would submit half of one line and leave the rest of the
// other behind.
func (s *Session) Type(line string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := io.WriteString(s.ptmx, line); err != nil {
		return err
	}
	time.Sleep(submitDelay)
	if _, err := io.WriteString(s.ptmx, "\r"); err != nil {
		return err
	}
	return nil
}

// Wait pumps the child's output to this terminal until it exits, then restores the
// terminal and reports the child's exit code. A non-zero code is not an error here — it
// is the child's answer, and `exec` passes it through.
func (s *Session) Wait() (int, error) {
	_, _ = io.Copy(os.Stdout, s.ptmx) // returns when the child closes the pty
	err := s.cmd.Wait()
	s.close()

	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0, nil
	case errors.As(err, &ee):
		return ee.ExitCode(), nil
	default:
		return 1, err
	}
}

// close undoes everything Start put in place. It is safe to call twice.
func (s *Session) close() {
	select {
	case <-s.stopWin:
	default:
		close(s.stopWin)
	}
	if s.restore != nil {
		s.restore()
		s.restore = nil
	}
	_ = s.ptmx.Close()
}
