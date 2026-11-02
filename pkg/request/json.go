// Package request contains reusable HTTP request decoding helpers.
package request

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"

	"github.com/hieuvu0611/commercelab-retail-be/pkg/response"
)

const MaxJSONBodyBytes int64 = 1 << 20

// DecodeJSON accepts one JSON object, rejects unknown fields and writes a safe
// error response when the content type, size or syntax is invalid.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		response.Error(w, r, http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE", "Content-Type must be application/json", nil)
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxJSONBodyBytes)
	decoder := json.NewDecoder(r.Body)
	var raw json.RawMessage
	if err = decoder.Decode(&raw); err == nil {
		var extra any
		err = decoder.Decode(&extra)
		if errors.Is(err, io.EOF) && bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) {
			objectDecoder := json.NewDecoder(bytes.NewReader(raw))
			objectDecoder.DisallowUnknownFields()
			if objectDecoder.Decode(dst) == nil {
				return true
			}
		}
	}
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		response.Error(w, r, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "Request body is too large", nil)
	} else {
		response.Error(w, r, http.StatusBadRequest, "INVALID_JSON", "Request body must contain one valid JSON object", nil)
	}
	return false
}
