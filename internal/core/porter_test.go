package core

import (
	"testing"
)

func TestIsConsonantYAtStart(t *testing.T) {
	// y at position 0 is a consonant
	if !isConsonant("y", 0) {
		t.Error("y at start should be consonant")
	}
}

func TestIsConsonantYAfterVowel(t *testing.T) {
	// y after a vowel (a) should be consonant
	if !isConsonant("ay", 1) {
		t.Error("y after vowel 'a' should be consonant")
	}
}

func TestIsConsonantYAfterConsonant(t *testing.T) {
	// y after consonant (b) should be vowel
	if isConsonant("by", 1) {
		t.Error("y after consonant 'b' should be vowel")
	}
}

func TestStemY(t *testing.T) {
	// Stem "fly" - y is a consonant at start, no vowel in "fl", so no step 1c
	result := Stem("fly")
	if result != "fly" {
		t.Errorf("Stem(fly) = %q, want fly", result)
	}
}

func TestStemHappy(t *testing.T) {
	// "happy" -> "happi" (Step 1c: y -> i because containsVowel(happ))
	result := Stem("happy")
	if result != "happi" {
		t.Errorf("Stem(happy) = %q, want happi", result)
	}
}

func TestStemCry(t *testing.T) {
	// "cry" -> "cry" - stem "cr" has no vowel, so step 1c doesn't apply
	result := Stem("cry")
	if result != "cry" {
		t.Errorf("Stem(cry) = %q, want cry", result)
	}
}

func TestStemCopy(t *testing.T) {
	// "copy" -> "copi" (Step 1c)
	result := Stem("copy")
	if result != "copi" {
		t.Errorf("Stem(copy) = %q, want copi", result)
	}
}

func TestStemEmpty(t *testing.T) {
	result := Stem("")
	if result != "" {
		t.Errorf("Stem('') = %q, want ''", result)
	}
}

func TestStemNoVowelInStem(t *testing.T) {
	// "try" - stem "tr" has no vowel, so no step 1c
	result := Stem("try")
	if result != "try" {
		t.Errorf("Stem(try) = %q, want try", result)
	}
}
