package utils

import (
	"fmt"
	"os"
	"strconv"

	"gopkg.in/yaml.v3"
)

// can't add a new sequency node right now - but not needed
func setYamlNode(root *yaml.Node, path []string, value *yaml.Node) (err error) {
	if len(path) == 0 {
		*root = *value
		return
	}
	key := path[0]
	rest := path[1:]
	//	fmt.Printf("key: %s rest: %v\n", key, rest)
	switch root.Kind {
	case yaml.DocumentNode:
		//		fmt.Printf("DocumentNode\n")
		setYamlNode(root.Content[0], path, value)
	case yaml.MappingNode:
		//		fmt.Printf("MappingNode\n")
		for i := 0; i < len(root.Content); i += 2 {
			if root.Content[i].Value == key {
				setYamlNode(root.Content[i+1], rest, value)
				return
			}
		}
		var keyN yaml.Node
		keyN.SetString(key)
		root.Content = append(root.Content, &keyN) //, value)
		if len(rest) > 0 {
			var newMap yaml.Node
			newMap.Kind = yaml.MappingNode
			root.Content = append(root.Content, &newMap)
		} else {
			root.Content = append(root.Content, value)
		}
		setYamlNode(root.Content[len(root.Content)-1], rest, value)
		return
	case yaml.SequenceNode:
		//		fmt.Printf("SequenceNode\n")
		index, err := strconv.Atoi(key)
		if err != nil {
			return fmt.Errorf("sequence key must be an integer")
		}
		setYamlNode(root.Content[index], rest, value)
	}
	return
}

func ModifyYamlFile(filename string, path []string, value *yaml.Node) (err error) {
	dat, err := os.ReadFile(filename)

	if err != nil {
		err = fmt.Errorf("error reading file %s: %s", filename, err.Error())
		return
	}
	var doc yaml.Node
	err = yaml.Unmarshal(dat, &doc)
	if err != nil {
		err = fmt.Errorf("error parsing file %s: %s", filename, err.Error())
		return
	}

	err = setYamlNode(&doc, path, value)

	if err != nil {
		err = fmt.Errorf("error setting value in file %s: %s", filename, err.Error())
		return
	}

	dat, err = yaml.Marshal(&doc)
	if err != nil {
		err = fmt.Errorf("error marshalling yaml: %s", err.Error())
		return
	}

	err = os.WriteFile(filename, dat, 0644)
	if err != nil {
		err = fmt.Errorf("error writing file %s: %s", filename, err.Error())
		return
	}

	return
}
