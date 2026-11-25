package mcp

import (
	"context"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
)

func TestManager_InstructionsByServer(t *testing.T) {
	server := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "0"},
		&sdk.ServerOptions{Instructions: "Call bookmark when the user decides."})
	mgr := NewManager(map[string]ServerConfig{"stitch": {}, "off": {Disabled: true}}, false)
	mgr.dialFn = inMemoryDial(t, server)
	startAndWait(t, mgr, context.Background())

	assert.Equal(t, map[string]string{"stitch": "Call bookmark when the user decides."}, mgr.InstructionsByServer())
}
