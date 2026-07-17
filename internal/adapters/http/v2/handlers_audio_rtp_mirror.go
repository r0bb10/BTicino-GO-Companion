package v2

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

type audioRTPMirrorRequest struct {
	Format string `json:"format"`
	Port   int    `json:"port"`
}

func (r *Router) handleAudioRTPMirror(w http.ResponseWriter, req *http.Request) {
	if r.audioMirror == nil {
		writeError(w, http.StatusServiceUnavailable, "audio_rtp_mirror_unavailable", "audio RTP mirror unavailable")
		return
	}

	switch req.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, r.audioMirror.AudioRTPMirrorStatus())
	case http.MethodPut:
		var body audioRTPMirrorRequest
		dec := json.NewDecoder(io.LimitReader(req.Body, 16*1024))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_json", "invalid JSON body")
			return
		}
		status, err := r.audioMirror.ConfigureAudioRTPMirror(strings.ToLower(strings.TrimSpace(body.Format)), body.Port)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_audio_rtp_mirror", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, status)
	case http.MethodDelete:
		writeJSON(w, http.StatusOK, r.audioMirror.ClearAudioRTPMirror())
	default:
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
	}
}
