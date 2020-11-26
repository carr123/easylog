package easylog

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"
)

type EasyLog struct {
	SaveDir       string
	FileName      string
	MaxFileSize   int64
	MaxFileCount  int64
	FlushFreq     time.Duration
	pool          sync.Pool
	Pipe          chan *bytes.Buffer
	nofityDelFile func()
}

func NewLog(buflen int, FlushFreq time.Duration) *EasyLog {
	if buflen < 10 {
		buflen = 10
	}

	if FlushFreq < time.Millisecond*10 {
		FlushFreq = time.Millisecond * 10
	}

	ins := &EasyLog{}
	ins.SaveDir = ""
	ins.FileName = "log.txt"
	ins.MaxFileSize = 1024 * 1024 * 4
	ins.MaxFileCount = 0
	ins.FlushFreq = FlushFreq
	ins.pool.New = func() interface{} {
		c := &bytes.Buffer{}
		return c
	}

	ins.Pipe = make(chan *bytes.Buffer, buflen)
	ins._initFileRemove()

	go ins._serveLog()

	return ins
}

//set where to store logs, and the log file's name
func (t *EasyLog) SetDir(szDir string, FileName string) error {
	if err := os.MkdirAll(szDir, 666); err != nil {
		return err
	}

	t.SaveDir = szDir
	t.FileName = FileName

	return nil
}

//set single file max size. if the file size exceeds MaxFileSize, then a new file
//will be created to store log info
func (t *EasyLog) SetMaxFileSize(MaxFileSize int64) error {
	if MaxFileSize < 1024*1024 {
		MaxFileSize = 1024 * 1024
	}

	t.MaxFileSize = MaxFileSize

	return nil
}

//set max log file count
//if MaxFileCount == 0, no file count limited.
//if MaxFileCount > 0 and actual file count > MaxFileCount, then the earliest log file will be deleted.
func (t *EasyLog) SetMaxFileCount(MaxFileCount int64) error {
	if MaxFileCount < 0 {
		MaxFileCount = 0
	}

	t.MaxFileCount = MaxFileCount

	return nil
}

func (t *EasyLog) Write(p []byte) (n int, err error) {
	buf := t.pool.Get().(*bytes.Buffer)
	buf.Reset()
	n, err = buf.Write(p)

	t.Pipe <- buf

	return
}

func (t *EasyLog) _initFileRemove() {
	ch := make(chan int, 1)

	cleanFile := func() {
		defer func() {
			recover()
		}()

		expr := fmt.Sprintf(`%s\.\d{14}`, t.FileName)
		re, _ := regexp.Compile(expr)
		flist := make([]string, 0, 100)
		filepath.Walk(t.SaveDir, func(path string, fi os.FileInfo, err error) error {
			if nil == fi {
				return nil
			}

			if fi.IsDir() {
				return nil
			}

			if re.MatchString(fi.Name()) {
				flist = append(flist, fi.Name())
			}

			return nil
		})

		if t.MaxFileCount <= 0 {
			return
		}

		if int64(len(flist))+1 <= t.MaxFileCount {
			return
		}

		sort.Slice(flist, func(i, j int) bool {
			return flist[i] < flist[j]
		})

		for i := 0; i < len(flist)+1-int(t.MaxFileCount); i++ {
			os.Remove(filepath.Join(t.SaveDir, flist[i]))
		}
	}

	go func() {
		for {
			<-ch
			cleanFile()
		}
	}()

	t.nofityDelFile = func() {
		if len(ch) == 0 {
			ch <- 1
		}
	}
}

func (t *EasyLog) _rename() {
	oldpath := filepath.Join(t.SaveDir, t.FileName)
	newname := fmt.Sprintf("%s.%s", t.FileName, time.Now().Format("20060102150405"))
	newpath := filepath.Join(t.SaveDir, newname)

	for i := 0; i < 2; i++ {
		if err := os.Rename(oldpath, newpath); err == nil {
			return
		}
		time.Sleep(time.Second)
	}

}

func (t *EasyLog) _tryWrite(data *bytes.Buffer) bool {
	fullPath := filepath.Join(t.SaveDir, t.FileName)
	f, err := os.OpenFile(fullPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, os.ModePerm|os.ModeTemporary)
	if err != nil {
		return true
	}

	defer f.Close()

	info, _ := f.Stat()
	fsize := info.Size()
	if fsize+int64(data.Len()) > t.MaxFileSize {
		return false
	}

	io.Copy(f, data)

	return true
}

func (t *EasyLog) _mustWrite(data *bytes.Buffer) {
	fullPath := filepath.Join(t.SaveDir, t.FileName)
	f, err := os.OpenFile(fullPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, os.ModePerm|os.ModeTemporary)
	if err != nil {
		return
	}

	defer f.Close()

	io.Copy(f, data)

	return
}

func (t *EasyLog) _writeFile(data *bytes.Buffer) {
	if t._tryWrite(data) {
		return
	}

	t._rename()
	t._mustWrite(data)
	t.nofityDelFile()

	return
}

func (t *EasyLog) _serveLog() {
	CalcMaxCacheSize := func() int {
		nMax := int(t.MaxFileSize)
		if nMax > 1024*1024*1 {
			nMax = 1024 * 1024 * 1
		}
		return nMax
	}

	do := func() {
		defer func() {
			recover()
		}()

		maxCacheSize := CalcMaxCacheSize()
		data := &bytes.Buffer{}

		tm := time.NewTicker(t.FlushFreq)
		for {
			select {
			case v, ok := <-t.Pipe:
				if ok {
					data.Write(v.Bytes())
					v.Reset()
					t.pool.Put(v)
				}
			case <-tm.C:
				if data.Len() > 0 {
					t._writeFile(data)
					data.Reset()
				}
				maxCacheSize = CalcMaxCacheSize()
			}

			if data.Len() > maxCacheSize {
				<-tm.C
				t._writeFile(data)
				data.Reset()
			}
		}
	}

	for {
		do()
	}

	return
}
