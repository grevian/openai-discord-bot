package storage

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"
)

// trackingBody records whether the body was closed and how many bytes were
// read from it before that.
type trackingBody struct {
	io.Reader
	closed bool
}

func (b *trackingBody) Close() error {
	b.closed = true
	return nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGetImageFromURLClosesBodyOnNon200(t *testing.T) {
	body := &trackingBody{Reader: bytes.NewReader([]byte("<Error>NoSuchKey</Error>"))}
	s := &ImageStorage{httpClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusNotFound, Body: body, Header: http.Header{}}, nil
	})}}

	rc, _, err := s.GetImageFromURL(context.Background(), "https://cdn.example/missing.png")
	if err == nil {
		t.Fatal("expected an error for a 404 response")
	}
	if rc != nil {
		t.Errorf("no body should be returned on error, got %v", rc)
	}
	if !body.closed {
		t.Error("response body was not closed on a non-200 status")
	}
}

func TestGetImageFromURLDrainsBodyOnNon200(t *testing.T) {
	body := &trackingBody{Reader: bytes.NewReader(bytes.Repeat([]byte("x"), 1024))}
	s := &ImageStorage{httpClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusInternalServerError, Body: body, Header: http.Header{}}, nil
	})}}

	if _, _, err := s.GetImageFromURL(context.Background(), "https://cdn.example/broken.png"); err == nil {
		t.Fatal("expected an error for a 500 response")
	}
	if body.Reader.(*bytes.Reader).Len() != 0 {
		t.Error("response body was not drained, so the connection cannot be reused")
	}
	if !body.closed {
		t.Error("response body was not closed on a non-200 status")
	}
}
