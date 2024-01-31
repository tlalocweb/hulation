package utils

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestURLPathAlt(t *testing.T) {
	s := "http://localhost:8080/a/b/c"
	h, p := GetURLHostPath(s)
	assert.Equal(t, "localhost:8080", h)
	assert.Equal(t, "/a/b/c", p)
}

func TestURLPathAlt2(t *testing.T) {
	s := "http://localhost:8080"
	h, p := GetURLHostPath(s)
	assert.Equal(t, "localhost:8080", h)
	assert.Equal(t, "", p)
}
func TestURLPathAlt3(t *testing.T) {
	s := "keep-alivealhost:8088/jobs/"
	h, p := GetURLHostPath(s)
	assert.Equal(t, "keep-alivealhost:8088", h)
	assert.Equal(t, "/jobs", p)
}
func TestURLPathAlt4(t *testing.T) {
	s := "keep-alivealhost:8088/"
	h, p := GetURLHostPath(s)
	assert.Equal(t, "keep-alivealhost:8088", h)
	assert.Equal(t, "", p)
}
