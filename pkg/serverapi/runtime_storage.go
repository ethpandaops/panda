package serverapi

import (
	"encoding/xml"
	"fmt"
	"strings"
)

type runtimeStorageListResult struct {
	XMLName  xml.Name `xml:"ListBucketResult"`
	Contents []struct {
		Key          string `xml:"Key"`
		Size         int64  `xml:"Size"`
		LastModified string `xml:"LastModified"`
	} `xml:"Contents"`
	NextContinuationToken string `xml:"NextContinuationToken"`
	IsTruncated           string `xml:"IsTruncated"`
}

func RuntimeStorageScopedPrefix(executionID, prefix string) string {
	relativePrefix := runtimeStorageRelativeKey(executionID, prefix)
	if relativePrefix == "" {
		return runtimeStorageExecutionPrefix(executionID)
	}

	return runtimeStorageExecutionPrefix(executionID) + relativePrefix
}

func RuntimeStorageScopedKey(executionID, key string) (string, string, error) {
	relativeKey := runtimeStorageRelativeKey(executionID, key)
	if relativeKey == "" {
		return "", "", fmt.Errorf("key is required")
	}

	return runtimeStorageExecutionPrefix(executionID) + relativeKey, relativeKey, nil
}

func ParseRuntimeStorageList(
	data []byte,
	executionID string,
	urlForKey func(string) string,
) ([]RuntimeStorageFile, string, error) {
	var result runtimeStorageListResult
	if err := xml.Unmarshal(data, &result); err != nil {
		return nil, "", err
	}

	executionPrefix := runtimeStorageExecutionPrefix(executionID)
	files := make([]RuntimeStorageFile, 0, len(result.Contents))
	for _, item := range result.Contents {
		if item.Key == "" || !strings.HasPrefix(item.Key, executionPrefix) {
			continue
		}

		relativeKey := strings.TrimPrefix(item.Key, executionPrefix)
		if relativeKey == "" {
			continue
		}

		files = append(files, RuntimeStorageFile{
			Key:          relativeKey,
			Size:         item.Size,
			LastModified: item.LastModified,
			URL:          urlForKey(item.Key),
		})
	}

	if strings.EqualFold(strings.TrimSpace(result.IsTruncated), "true") {
		return files, result.NextContinuationToken, nil
	}

	return files, "", nil
}

func runtimeStorageExecutionPrefix(executionID string) string {
	return strings.TrimLeft(strings.TrimSpace(executionID), "/") + "/"
}

func runtimeStorageRelativeKey(executionID, key string) string {
	trimmed := strings.TrimLeft(strings.TrimSpace(key), "/")
	if trimmed == "" {
		return ""
	}

	return strings.TrimPrefix(trimmed, runtimeStorageExecutionPrefix(executionID))
}
