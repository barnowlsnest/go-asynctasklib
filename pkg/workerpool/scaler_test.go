package workerpool

import (
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

const (
	testMinSize             = 1                      //nolint:unused // used by later tasks in this package
	testMaxSize             = 4                      //nolint:unused // used by later tasks in this package
	testScaleUpStep         = 1                      //nolint:unused // used by later tasks in this package
	testIdleHeadroom        = 1                      //nolint:unused // used by later tasks in this package
	testScalerInterval      = 100 * time.Millisecond //nolint:unused // used by later tasks in this package
	testScaleDownCooldown   = 5 * time.Second        //nolint:unused // used by later tasks in this package
	testScaleDownIdlePeriod = 2 * time.Second        //nolint:unused // used by later tasks in this package
)

type ScalerTestSuite struct {
	suite.Suite
}

func TestScalerSuite(t *testing.T) {
	suite.Run(t, new(ScalerTestSuite))
}

func (s *ScalerTestSuite) TestAutoScaleConfigDefaults() {
	cases := []struct {
		name     string
		in       AutoScaleConfig
		expected AutoScaleConfig
	}{
		{
			name: "all zero -> defaults",
			in:   AutoScaleConfig{},
			expected: AutoScaleConfig{
				MinSize:             1,
				MaxSize:             runtime.NumCPU(),
				ScaleUpStep:         1,
				Interval:            100 * time.Millisecond,
				IdleHeadroom:        1,
				ScaleUpCooldown:     0,
				ScaleDownCooldown:   5 * time.Second,
				ScaleDownIdlePeriod: 2 * time.Second,
			},
		},
		{
			name: "explicit values preserved",
			in: AutoScaleConfig{
				MinSize: 2, MaxSize: 8, ScaleUpStep: 3, Interval: time.Second,
				IdleHeadroom: 2, ScaleUpCooldown: time.Second,
				ScaleDownCooldown: 10 * time.Second, ScaleDownIdlePeriod: 4 * time.Second,
			},
			expected: AutoScaleConfig{
				MinSize: 2, MaxSize: 8, ScaleUpStep: 3, Interval: time.Second,
				IdleHeadroom: 2, ScaleUpCooldown: time.Second,
				ScaleDownCooldown: 10 * time.Second, ScaleDownIdlePeriod: 4 * time.Second,
			},
		},
		{
			name: "negative normalized like zero",
			in:   AutoScaleConfig{MinSize: -5, MaxSize: -1},
			expected: AutoScaleConfig{
				MinSize:             1,
				MaxSize:             runtime.NumCPU(),
				ScaleUpStep:         1,
				Interval:            100 * time.Millisecond,
				IdleHeadroom:        1,
				ScaleUpCooldown:     0,
				ScaleDownCooldown:   5 * time.Second,
				ScaleDownIdlePeriod: 2 * time.Second,
			},
		},
	}

	for i := range cases {
		tc := cases[i]
		s.Run(tc.name, func() {
			got := tc.in
			got.applyDefaults()
			s.Require().Equal(tc.expected, got)
		})
	}
}

func (s *ScalerTestSuite) TestAutoScaleConfigValidate() {
	cases := []struct {
		name        string
		in          AutoScaleConfig
		expectedErr bool
	}{
		{name: "min below max ok", in: AutoScaleConfig{MinSize: 1, MaxSize: 4}, expectedErr: false},
		{name: "min equals max ok", in: AutoScaleConfig{MinSize: 4, MaxSize: 4}, expectedErr: false},
		{name: "min above max invalid", in: AutoScaleConfig{MinSize: 5, MaxSize: 4}, expectedErr: true},
	}

	for _, tc := range cases {
		s.Run(tc.name, func() {
			err := tc.in.validate()
			if tc.expectedErr {
				s.Require().Error(err)
			} else {
				s.Require().NoError(err)
			}
		})
	}
}
