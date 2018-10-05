// Copyright 2016 The kingshard Authors. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License"): you may
// not use this file except in compliance with the License. You may obtain
// a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS, WITHOUT
// WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the
// License for the specific language governing permissions and limitations
// under the License.

package golog

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

//log level, from low to high, more high means more serious
const (
	LevelTrace = iota
	LevelDebug
	LevelInfo
	LevelWarn
	LevelError
	LevelFatal
)

const (
	Ltime  = 1 << iota //time format "2006/01/02 15:04:05"
	Lfile              //file.go:123
	Llevel             //[Trace|Debug|Info...]
)

var LevelName [6]string = [6]string{"TRACE", "DEBUG", "INFO", "WARN", "ERROR", "FATAL"}

const (
	TimeFormat     = "2006/01/02 15:04:05"
	maxBufPoolSize = 16
)

type Logger struct {
	sync.Mutex

	level int
	flag  int

	handler Handler

	quit chan struct{}
	msg  chan []byte

	bufs [][]byte

	wg sync.WaitGroup

	closed bool
}

//new a logger with specified handler and flag
func New(handler Handler, flag int) *Logger {
	var l = new(Logger)

	l.level = LevelInfo
	l.handler = handler

	l.flag = flag

	l.quit = make(chan struct{})
	l.closed = false

	l.msg = make(chan []byte, 1024)

	l.bufs = make([][]byte, 0, 16)

	l.wg.Add(1)
	go l.run()

	return l
}

//new a default logger with specified handler and flag: Ltime|Lfile|Llevel
func NewDefault(handler Handler) *Logger {
	return New(handler, Ltime|Lfile|Llevel)
}

func newStdHandler() *StreamHandler {
	h, _ := NewStreamHandler(os.Stdout)
	return h
}

var std = NewDefault(newStdHandler())

func Close() {
	std.Close()
}

func SetLevel(level int) {
	std.SetLevel(level)
}

func StdLogger() *Logger {
	return std
}

func GetLevel() int {
	return std.level
}

func (l *Logger) run() {
	defer l.wg.Done()
	for {
		select {
		case msg := <-l.msg:
			l.handler.Write(msg)
			l.putBuf(msg)
		case <-l.quit:
			if len(l.msg) == 0 {
				return
			}
		}
	}
}

func (l *Logger) popBuf() []byte {
	l.Lock()
	var buf []byte
	if len(l.bufs) == 0 {
		buf = make([]byte, 0, 1024)
	} else {
		buf = l.bufs[len(l.bufs)-1]
		l.bufs = l.bufs[0 : len(l.bufs)-1]
	}
	l.Unlock()

	return buf
}

func (l *Logger) putBuf(buf []byte) {
	l.Lock()
	if len(l.bufs) < maxBufPoolSize {
		buf = buf[0:0]
		l.bufs = append(l.bufs, buf)
	}
	l.Unlock()
}

func (l *Logger) Close() {
	if l.closed {
		return
	}
	l.closed = true

	close(l.quit)
	l.wg.Wait()
	l.quit = nil

	l.handler.Close()
}

//set log level, any log level less than it will not log
func (l *Logger) SetLevel(level int) {
	l.level = level
}

func (l *Logger) Level() int {
	return l.level
}

//a low interface, maybe you can use it for your special log format
//but it may be not exported later......
func (l *Logger) Output(callDepth int, level int, format string, v ...interface{}) {
	if l.level > level {
		return
	}

	buf := l.popBuf()

	if l.flag&Ltime > 0 {
		now := time.Now().Format(TimeFormat)
		buf = append(buf, now...)
		buf = append(buf, " - "...)
	}

	if l.flag&Llevel > 0 {
		buf = append(buf, LevelName[level]...)
		buf = append(buf, " - "...)
	}

	if l.flag&Lfile > 0 {
		_, file, line, ok := runtime.Caller(callDepth)
		if !ok {
			file = "???"
			line = 0
		} else {
			for i := len(file) - 1; i > 0; i-- {
				if file[i] == '/' {
					file = file[i+1:]
					break
				}
			}
		}

		buf = append(buf, file...)
		buf = append(buf, ":["...)

		buf = strconv.AppendInt(buf, int64(line), 10)
		buf = append(buf, "] - "...)
	}

	s := fmt.Sprintf(format, v...)

	buf = append(buf, s...)

	if s[len(s)-1] != '\n' {
		buf = append(buf, '\n')
	}

	l.msg <- buf
}

func (l *Logger) Write(p []byte) (n int, err error) {
	output(LevelInfo, "web", "api", string(p), 0)
	return len(p), nil
}

func escape(s string, filterEqual bool) string {
	dest := make([]byte, 0, 2*len(s))
	for i := 0; i < len(s); i++ {
		r := s[i]
		switch r {
		case '|':
			continue
		case '%':
			dest = append(dest, '%', '%')
		case '=':
			if !filterEqual {
				dest = append(dest, '=')
			}
		default:
			dest = append(dest, r)
		}
	}

	return string(dest)
}

//全局变量
var sysLogger *Logger = StdLogger()

func SetGoLoger(newLog *Logger, level string) {
	sysLogger = newLog
	switch strings.ToLower(level) {
	case "debug":
		sysLogger.SetLevel(LevelDebug)
	case "info":
		sysLogger.SetLevel(LevelInfo)
	case "warn":
		sysLogger.SetLevel(LevelWarn)
	case "error":
		sysLogger.SetLevel(LevelError)
	default:
		sysLogger.SetLevel(LevelError)
	}
}

func output(level int, module string, method string, msg string, args ...interface{}) {
	if level < sysLogger.Level() {
		return
	}
	//
	var argsBuff bytes.Buffer
	var content string
	if strings.Contains(msg, "%") {
		content = fmt.Sprintf(`[%s] "%s" `,
			module, method) + fmt.Sprintf(msg, args...)
	} else {
		num := len(args) / 2
		for i := 0; i < num; i++ {
			argsBuff.WriteString(escape(fmt.Sprintf("%v=%v", args[i*2], args[i*2+1]), false))
			if (i+1)*2 != len(args) {
				argsBuff.WriteString("|")
			}
		}
		if len(args)%2 == 1 {
			argsBuff.WriteString(escape(fmt.Sprintf("%v", args[len(args)-1]), false))
		}
		content = fmt.Sprintf(`[%s] "%s" "%s" "%s"`,
			module, method, msg, argsBuff.String())
	}

	sysLogger.Output(3, level, content)
}

func Trace(module string, method string, msg string, args ...interface{}) {
	output(LevelTrace, module, method, msg, args...)
}
func Debug(module string, method string, msg string, args ...interface{}) {
	output(LevelDebug, module, method, msg, args...)
}
func Info(module string, method string, msg string, args ...interface{}) {
	output(LevelInfo, module, method, msg, args...)
}
func Warn(module string, method string, msg string, args ...interface{}) {
	output(LevelWarn, module, method, msg, args...)
}
func Error(module string, method string, msg string, args ...interface{}) {
	output(LevelError, module, method, msg, args...)
}
func Fatal(module string, method string, msg string, args ...interface{}) {
	output(LevelFatal, module, method, msg, args...)
}

var (
	dunno     = []byte("???")
	centerDot = []byte("·")
	dot       = []byte(".")
	slash     = []byte("/")
)

func Stack(skip int) []byte {
	buf := new(bytes.Buffer) // the returned data
	// As we loop, we open files and read them. These variables record the currently
	// loaded file.
	var lines [][]byte
	var lastFile string
	for i := skip; ; i++ {
		// Skip the expected number of frames
		pc, file, line, ok := runtime.Caller(i)
		if !ok {
			break
		}
		// Print this much at least.  If we can't find the source, it won't show.
		fmt.Fprintf(buf, "%s:%d (0x%x)\n", file, line, pc)
		if file != lastFile {
			data, err := ioutil.ReadFile(file)
			if err != nil {
				continue
			}
			lines = bytes.Split(data, []byte{'\n'})
			lastFile = file
		}
		fmt.Fprintf(buf, "\t%s: %s\n", function(pc), source(lines, line))
	}
	return buf.Bytes()
}

// source returns a space-trimmed slice of the n'th line.
func source(lines [][]byte, n int) []byte {
	n-- // in stack trace, lines are 1-indexed but our array is 0-indexed
	if n < 0 || n >= len(lines) {
		return dunno
	}
	return bytes.TrimSpace(lines[n])
}

// function returns, if possible, the name of the function containing the PC.
func function(pc uintptr) []byte {
	fn := runtime.FuncForPC(pc)
	if fn == nil {
		return dunno
	}
	name := []byte(fn.Name())
	// The name includes the path name to the package, which is unnecessary
	// since the file name is already included.  Plus, it has center dots.
	// That is, we see
	//	runtime/debug.*T·ptrmethod
	// and want
	//	*T.ptrmethod
	// Also the package path might contains dot (e.g. code.google.com/...),
	// so first eliminate the path prefix
	if lastslash := bytes.LastIndex(name, slash); lastslash >= 0 {
		name = name[lastslash+1:]
	}
	if period := bytes.Index(name, dot); period >= 0 {
		name = name[period+1:]
	}
	name = bytes.Replace(name, centerDot, dot, -1)
	return name
}
