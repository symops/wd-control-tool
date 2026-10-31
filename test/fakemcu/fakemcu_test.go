package fakemcu

import (
	"io"
	"testing"
	"time"
)

// readExactly is the whole reason this package stopped using
// (*os.File).SetReadDeadline (see its doc comment in fakemcu.go) -
// pin both directions directly: it returns promptly when the data
// arrives, and it returns promptly (not after some much longer
// hang) when the data never arrives at all.

func TestReadExactly_Success(t *testing.T) {
	r, w := io.Pipe()
	defer r.Close()
	defer w.Close()

	go func() { w.Write([]byte{0x01, 0x02, 0x03}) }()

	buf, err := readExactly(r, 3, time.Second)
	if err != nil {
		t.Fatalf("readExactly: %v", err)
	}
	want := []byte{0x01, 0x02, 0x03}
	for i, b := range want {
		if buf[i] != b {
			t.Errorf("buf[%d] = 0x%02X, want 0x%02X", i, buf[i], b)
		}
	}
}

func TestReadExactly_PartialThenMoreData(t *testing.T) {
	r, w := io.Pipe()
	defer r.Close()
	defer w.Close()

	go func() {
		w.Write([]byte{0x01})
		time.Sleep(20 * time.Millisecond)
		w.Write([]byte{0x02, 0x03})
	}()

	buf, err := readExactly(r, 3, time.Second)
	if err != nil {
		t.Fatalf("readExactly: %v", err)
	}
	if len(buf) != 3 || buf[0] != 0x01 || buf[1] != 0x02 || buf[2] != 0x03 {
		t.Errorf("buf = % X, want 01 02 03", buf)
	}
}

// readExactly's error path (as opposed to its timeout path, covered
// below) had no test: if the reader fails outright - here, the write
// side closes after only a partial write, so the next Read returns
// io.ErrClosedPipe before n bytes ever arrive - that error must come
// back promptly, not be swallowed into (or confused with) a generic
// timeout after waiting out the full duration.
func TestReadExactly_ReaderErrorPropagatesPromptly(t *testing.T) {
	r, w := io.Pipe()
	defer r.Close()

	go func() {
		w.Write([]byte{0x01}) // only 1 of the 3 bytes readExactly wants
		w.Close()
	}()

	const timeout = 5 * time.Second
	start := time.Now()
	_, err := readExactly(r, 3, timeout)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("readExactly: want an error (closed pipe before enough data arrived), got nil")
	}
	if elapsed > time.Second {
		t.Errorf("readExactly took %v to report the reader error, want well under the %v timeout (it shouldn't need to wait that out at all)", elapsed, timeout)
	}
}

// The actual regression test: nothing is ever written, so this must
// return an error close to `timeout`, not hang indefinitely (an
// earlier SetReadDeadline-based version of this package's ReadFrame
// did exactly that - confirmed by watching it block 5+ seconds past
// a 500ms deadline). Bound the elapsed time generously (an order of
// magnitude above the requested timeout) so this isn't flaky under
// load, while still catching the real failure mode (seconds-to-
// indefinite hangs), which is a difference of orders of magnitude.
func TestReadExactly_TimesOutPromptlyWhenNothingArrives(t *testing.T) {
	r, w := io.Pipe()
	defer r.Close()
	defer w.Close() // never written to

	const timeout = 100 * time.Millisecond
	start := time.Now()
	_, err := readExactly(r, 1, timeout)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("readExactly: want a timeout error, got nil")
	}
	if elapsed > 10*timeout {
		t.Errorf("readExactly took %v to time out, want close to %v (this is the exact failure mode that made an earlier version of ReadFrame hang)", elapsed, timeout)
	}
}
