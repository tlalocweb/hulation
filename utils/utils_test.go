package utils

import (
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"gopkg.in/yaml.v3"
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

type TestStructYAMLDocModify struct {
	Hello struct {
		World []struct {
			Name string `yaml:"name"`
			Info struct {
				Data1 int    `yaml:"data1"`
				Data2 int    `yaml:"data2"`
				Data3 string `yaml:"data3"`
				Data4 struct {
					Innerdat string `yaml:"innerdat"`
				} `yaml:"data4"`
			} `yaml:"info"`
		} `yaml:"world"`
	} `yaml:"hello"`
}

func TestYAMLDocModify(t *testing.T) {
	var doc yaml.Node

	var inputString = `
hello:
# some comment
  world:
    - name: John
      info:
    - name: Name
      info: 
        data1: 123
        data2: 456
`

	var path = []string{"hello", "world", "1", "info", "data3"}
	err := yaml.Unmarshal([]byte(inputString), &doc)
	if err != nil {
		t.Errorf("error parsing input string: %s", err.Error())
		return
	}

	newVal := yaml.Node{
		//		Tag:   "data3",
		Kind:  yaml.ScalarNode,
		Value: "test2",
	}

	err = setYamlNode(&doc, path, &newVal)

	if err != nil {
		t.Errorf("error setting value in input string: %s", err.Error())
		return
	}

	dat, err := yaml.Marshal(&doc)
	if err != nil {
		t.Errorf("error marshalling yaml: %s", err.Error())
		return
	}

	outputString := string(dat)

	fmt.Printf("outputString: %s\n", outputString)
	// Use the outputString as needed
	var testStruct TestStructYAMLDocModify
	yaml.Unmarshal([]byte(outputString), &testStruct)
	assert.Equal(t, "test2", testStruct.Hello.World[1].Info.Data3)
}

func TestYAMLDocModify2(t *testing.T) {
	var doc yaml.Node

	var inputString = `
hello:
# some comment
  world:
    - name: John
      info:
    - name: Name
      info: 
        data1: 123
        data2: 456
`

	var path = []string{"hello", "world", "1", "info", "data2"}
	err := yaml.Unmarshal([]byte(inputString), &doc)
	if err != nil {
		t.Errorf("error parsing input string: %s", err.Error())
		return
	}

	newVal := yaml.Node{
		//		Tag:   "data3",
		Kind:  yaml.ScalarNode,
		Value: "789",
	}

	err = setYamlNode(&doc, path, &newVal)

	if err != nil {
		t.Errorf("error setting value in input string: %s", err.Error())
		return
	}

	dat, err := yaml.Marshal(&doc)
	if err != nil {
		t.Errorf("error marshalling yaml: %s", err.Error())
		return
	}

	outputString := string(dat)

	fmt.Printf("outputString: %s\n", outputString)
	// Use the outputString as needed
	var testStruct TestStructYAMLDocModify
	yaml.Unmarshal([]byte(outputString), &testStruct)
	assert.Equal(t, 789, testStruct.Hello.World[1].Info.Data2)

}

func TestYAMLDocModify3(t *testing.T) {
	var doc yaml.Node

	var inputString = `
hello:
# some comment
  world:
    - name: John
      info:
    - name: Name
      info: 
        data1: 123
        data2: 456
`

	var path = []string{"hello", "world", "1", "info", "data4", "innerdat"}
	err := yaml.Unmarshal([]byte(inputString), &doc)
	if err != nil {
		t.Errorf("error parsing input string: %s", err.Error())
		return
	}

	newVal := yaml.Node{
		//		Tag:   "data3",
		Kind:  yaml.ScalarNode,
		Value: "test2",
	}

	err = setYamlNode(&doc, path, &newVal)

	if err != nil {
		t.Errorf("error setting value in input string: %s", err.Error())
		return
	}

	dat, err := yaml.Marshal(&doc)
	if err != nil {
		t.Errorf("error marshalling yaml: %s", err.Error())
		return
	}

	outputString := string(dat)

	fmt.Printf("outputString: %s\n", outputString)
	// Use the outputString as needed
	var testStruct TestStructYAMLDocModify
	yaml.Unmarshal([]byte(outputString), &testStruct)
	assert.Equal(t, "test2", testStruct.Hello.World[1].Info.Data4.Innerdat)
}

func TestYamlModifyDoc(t *testing.T) {
	var inputString = `
hello:
# some comment
  world:
    - name: John
      info:
    - name: Name
      info: 
        data1: 123
        data2: 456
`

	os.WriteFile("test.yaml", []byte(inputString), 0644)

	err := ModifyYamlFile("test.yaml", []string{"hello", "world", "1", "info", "data2"}, &yaml.Node{
		Kind:  yaml.ScalarNode,
		Value: "789",
	})

	if err != nil {
		t.Errorf("error setting value in input string: %s", err.Error())
		return
	}

	dat, err := os.ReadFile("test.yaml")
	if err != nil {
		t.Errorf("error reading file test.yaml: %s", err.Error())
		return
	}
	var testStruct TestStructYAMLDocModify
	yaml.Unmarshal([]byte(dat), &testStruct)
	assert.Equal(t, 789, testStruct.Hello.World[1].Info.Data2)

}
