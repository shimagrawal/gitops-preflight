package steps

import (
	"bytes"
	"errors"
	"io"
	"os"

	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
)

// decodeYAMLDocuments splits a multi-document YAML (or JSON) stream into maps.
// Empty documents are dropped.
func decodeYAMLDocuments(data []byte) ([]map[string]any, error) {
	dec := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	var docs []map[string]any
	for {
		var doc map[string]any
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			return docs, nil
		}
		if err != nil {
			return nil, err
		}
		if doc != nil {
			docs = append(docs, doc)
		}
	}
}

// ReadDocuments reads every document of a YAML file.
func ReadDocuments(path string) ([]map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return decodeYAMLDocuments(data)
}
