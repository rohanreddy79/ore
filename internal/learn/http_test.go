// SPDX-License-Identifier: AGPL-3.0-only

package learn

import (
	"io"
	"net/http"
)

type rawResponse struct {
	code   int
	header http.Header
	body   string
}

func newRequest(e *env, method, path string) (*http.Request, error) {
	req, err := http.NewRequest(method, e.srv.URL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+adminToken)
	return req, nil
}

func httpDo(req *http.Request) (rawResponse, error) {
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return rawResponse{}, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return rawResponse{code: resp.StatusCode, header: resp.Header, body: string(b)}, nil
}
