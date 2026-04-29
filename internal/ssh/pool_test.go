package ssh

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestFakeExecutor_Success(t *testing.T) {
	f := NewFakeExecutor()
	f.SetResponse("host-a", "ok\n", nil)

	out, err := f.Run(context.Background(), Target{Host: "host-a"}, "echo ok")
	require.NoError(t, err)
	require.Equal(t, "ok\n", out)
	require.Equal(t, 1, f.CallCount("host-a"))
}

func TestFakeExecutor_Failure(t *testing.T) {
	f := NewFakeExecutor()
	boom := errors.New("connection refused")
	f.SetResponse("host-b", "", boom)

	_, err := f.Run(context.Background(), Target{Host: "host-b"}, "echo ok")
	require.ErrorIs(t, err, boom)
}

func TestPing_UsesExecutor(t *testing.T) {
	f := NewFakeExecutor()
	f.SetResponse("host-c", "ok\n", nil)

	lat, err := Ping(context.Background(), f, Target{Host: "host-c"})
	require.NoError(t, err)
	require.Greater(t, lat, time.Duration(0))
}

func TestPing_WrapsExecutorError(t *testing.T) {
	f := NewFakeExecutor()
	f.SetResponse("host-d", "", errors.New("auth failed"))

	_, err := Ping(context.Background(), f, Target{Host: "host-d"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "auth failed")
}
