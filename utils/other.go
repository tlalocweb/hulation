package utils

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// SqlStr replace ^ or ” with `
func SqlStr(s string) (ret string) {
	ret = strings.ReplaceAll(s, "^", "`")
	ret = strings.ReplaceAll(ret, "”", "`")
	return
}

func CleanShutdown(exitval int) {
	os.Stderr.Sync()
	os.Stdout.Sync()
	os.Exit(exitval)
}

func GetHostOnly(host string) string {
	parts := strings.Split(host, ":")
	return parts[0]
}

var replaceExecPathRE = regexp.MustCompile(`(?m){{\s*huladir\s*}}`)

func ReadFileFromConfigPath(confpath string, filename string) (ret []byte, err error) {
	// process path
	mypath, err := os.Executable()
	if err != nil {
		return
	}
	mydir := filepath.Dir(mypath)
	//	fpath := strings.ReplaceAll(confpath, "{{huladir}}", mydir)
	fpath := replaceExecPathRE.ReplaceAllString(confpath, mydir)
	final := filepath.Join(fpath, filename)
	//	f, err := os.Open(final)
	ret, err = os.ReadFile(final)
	if err != nil {
		err = fmt.Errorf("error opening file %s: %s", final, err.Error())
		return
	}
	//	defer f.Close()

	return
}

const (
	restrURLPathAlt = `(?:http[s]?\:\/\/)?([^\/]+)(.*)`
)

var reURLPathAlt *regexp.Regexp

func init() {
	reURLPathAlt = regexp.MustCompile(restrURLPathAlt)
}

func GetURLHostPath(path string) (host string, urlpath string) {
	res := reURLPathAlt.FindAllStringSubmatch(path, -1)
	if len(res) > 0 && len(res[0]) > 1 {
		host = res[0][1]
		if len(res[0]) > 2 {
			urlpath = res[0][2]
			// trim the last char of the string if it is a /
			if len(urlpath) > 0 && urlpath[len(urlpath)-1] == '/' {
				urlpath = urlpath[:len(urlpath)-1]
			}
		}
	}
	return
}
