package main

import (
	"testing"
	"time"
)

func TestResultStruct(t *testing.T) {
	r := Result{
		Name:      "test",
		OpsPerSec: 100.5,
		Duration:  time.Second,
		Bytes:     1024,
	}

	if r.Name != "test" {
		t.Errorf("Name = %s, want 'test'", r.Name)
	}
	if r.OpsPerSec != 100.5 {
		t.Errorf("OpsPerSec = %f, want 100.5", r.OpsPerSec)
	}
	if r.Duration != time.Second {
		t.Errorf("Duration = %v, want 1s", r.Duration)
	}
	if r.Bytes != 1024 {
		t.Errorf("Bytes = %d, want 1024", r.Bytes)
	}
}

func TestBenchTokenizeSmall(t *testing.T) {
	results := benchTokenize()
	if len(results) == 0 {
		t.Fatal("benchTokenize returned no results")
	}

	r := results[0]
	if r.Name != "tokenize/small" {
		t.Errorf("Name = %s, want 'tokenize/small'", r.Name)
	}
	if r.OpsPerSec <= 0 {
		t.Error("OpsPerSec should be positive")
	}
	if r.Bytes <= 0 {
		t.Error("Bytes should be positive")
	}
}

func TestBenchTokenizeLarge(t *testing.T) {
	results := benchTokenize()
	if len(results) < 2 {
		t.Fatal("benchTokenize returned fewer than 2 results")
	}

	r := results[1]
	if r.Name != "tokenize/large" {
		t.Errorf("Name = %s, want 'tokenize/large'", r.Name)
	}
}

func TestBenchIndex(t *testing.T) {
	results := benchIndex()
	if len(results) == 0 {
		t.Fatal("benchIndex returned no results")
	}

	r := results[0]
	if r.Name != "index/add" {
		t.Errorf("Name = %s, want 'index/add'", r.Name)
	}
	if r.OpsPerSec <= 0 {
		t.Error("OpsPerSec should be positive")
	}
}

func TestBenchSearch(t *testing.T) {
	results := benchSearch()
	if len(results) == 0 {
		t.Fatal("benchSearch returned no results")
	}

	r := results[0]
	if r.Name != "search/bm25" {
		t.Errorf("Name = %s, want 'search/bm25'", r.Name)
	}
	if r.OpsPerSec <= 0 {
		t.Error("OpsPerSec should be positive")
	}
}

func TestBenchExec(t *testing.T) {
	results := benchExec()
	if len(results) == 0 {
		t.Fatal("benchExec returned no results")
	}

	r := results[0]
	if r.Name != "exec/echo" {
		t.Errorf("Name = %s, want 'exec/echo'", r.Name)
	}
}
