package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/mattn/go-isatty"

	tslc "github.com/NimbleMarkets/ntcharts/linechart/timeserieslinechart"
)

var (
	interval = flag.Duration("interval", 100*time.Millisecond, "Poll interval")
)

var leftBarChars = []string{
	" ",
	"▏",
	"▎",
	"▍",
	"▌",
	"▋",
	"▊",
	"▉",
	"█",
}

type winsize struct {
	Row    uint16
	Col    uint16
	Xpixel uint16
	Ypixel uint16
}

func getTerminalSize() (int, int, error) {
	ws := &winsize{}
	retCode, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		uintptr(syscall.Stdin),
		uintptr(syscall.TIOCGWINSZ),
		uintptr(unsafe.Pointer(ws)),
	)
	if int(retCode) == -1 {
		return 0, 0, errno
	}
	return int(ws.Col), int(ws.Row), nil
}

func termWidth() int {
	var width int
	if isatty.IsTerminal(os.Stdout.Fd()) {
		cols, _, err := getTerminalSize()
		if err != nil {
			width = 80
		} else {
			width = cols - 1
		}
	} else {
		width = 80
	}
	return width
}

func printMeter(label string, num, denom float64) {
	var frac float64
	if denom != 0 {
		frac = num / denom
	}

	pct := fmt.Sprintf("%.2f", 100*frac)
	fmt.Printf("%s\t%.2f/%.2f (%s%%)\n", label, num, denom, pct)

	width := termWidth()

	widthInEighths := width * 8
	remainingFilledWidthEighths := int(min(float64(widthInEighths)*frac, float64(widthInEighths)))

	for i := 0; i < width; i++ {
		cWidthEighths := int(min(float64(remainingFilledWidthEighths), 8))
		c := leftBarChars[cWidthEighths]
		remainingFilledWidthEighths -= cWidthEighths
		fmt.Printf("\x1b[97;100m%s\x1b[m", c)
	}

	if frac > 1 {
		fmt.Print("!")
	}
	fmt.Println()
}

func min(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func clear() {
	fmt.Print("\033[H\033[2J")
}

type PSI struct {
	SomeTotal uint64
}

type Result[T any] struct {
	Value T
	Err   error
}

func (r *Result[T]) Get() (T, error) {
	return r.Value, r.Err
}

func readPSI(path string) (*PSI, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	s := string(b)
	// Just read SomeTotal for now
	_, s, _ = strings.Cut(s, "total=")
	s, _, _ = strings.Cut(s, "\n")
	someTotal, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parse some.total: %w", err)
	}
	return &PSI{SomeTotal: uint64(someTotal)}, nil
}

type measurement struct {
	t    time.Time
	psis map[string]*PSI
}

type TimePoint = tslc.TimePoint

func drawCharts(paths []string, history map[string][]TimePoint) {
	width := termWidth()
	height := 10
	for _, p := range paths {
		chart := tslc.New(width, height, tslc.WithYRange(0, 100))
		for _, tp := range history[p] {
			chart.PushDataSet(p, tp)
		}
		chart.DrawBrailleAll()
		fmt.Println(p)
		fmt.Println(chart.Canvas.View())
	}
}

func run() error {
	paths := []string{"/proc/pressure/cpu", "/proc/pressure/io", "/proc/pressure/memory"} // TODO: irq

	type req struct {
		res chan Result[*PSI]
	}
	reqs := map[string]chan req{}
	for _, path := range paths {
		ch := make(chan req)
		reqs[path] = ch
		go func() {
			for req := range ch {
				psi, err := readPSI(path)
				req.res <- Result[*PSI]{Value: psi, Err: err}
			}
		}()
	}

	getPSIs := func() (map[string]*PSI, error) {
		m := make(map[string]*PSI, len(paths))
		rsps := make(map[string]chan Result[*PSI], len(paths))
		for _, p := range paths {
			rsps[p] = make(chan Result[*PSI], 1)
			reqs[p] <- req{res: rsps[p]}
		}
		for _, p := range paths {
			res := <-rsps[p]
			psi, err := res.Get()
			if err != nil {
				return nil, err
			}
			m[p] = psi
		}
		return m, nil
	}

	history := map[string][]TimePoint{}

	var last *measurement
	for {
		time.Sleep(*interval)
		t := time.Now()
		psis, err := getPSIs()
		if err != nil {
			return err
		}
		if last != nil {
			clear()
			for _, p := range paths {
				dur := t.Sub(last.t)
				acc := time.Duration(psis[p].SomeTotal-last.psis[p].SomeTotal) * time.Microsecond
				pct := float64(acc) / float64(dur) * 100
				printMeter(p, pct, 100)
				history[p] = append(history[p], TimePoint{Time: t, Value: pct})
				const limit = 1024
				if len(history[p]) > limit {
					history[p] = history[p][1:]
				}
			}
		}
		last = &measurement{t, psis}
		if len(history) > 0 {
			drawCharts(paths, history)
		}
	}

	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		os.Exit(1)
	}
}
