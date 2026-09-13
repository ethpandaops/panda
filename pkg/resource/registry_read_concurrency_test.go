package resource

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/panda/pkg/surface"
)

func silentLog() logrus.FieldLogger {
	l := logrus.New()
	l.SetLevel(logrus.PanicLevel)
	return l
}

// pausingToolLister blocks inside List so a test can hold the getting-started
// handler open at the exact point between resolving the resource and the
// handler listing other resources. The real handler calls ToolLister.List
// immediately before it lists registry resources.
type pausingToolLister struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (p *pausingToolLister) List() []mcp.Tool {
	p.once.Do(func() { close(p.entered) })
	<-p.release
	return []mcp.Tool{{Name: "execute_python"}}
}

// A resource handler that lists other resources must not block a concurrent
// register. The getting-started handler does exactly this, and a registry
// refresh registers resources while requests are being served. This drives the
// real handler to the moment it lists resources, registers a resource in
// parallel, and requires the read to still complete.
func TestRegistryRead_HandlerListsResources_ConcurrentRegister(t *testing.T) {
	reg := NewRegistry(silentLog())
	tl := &pausingToolLister{entered: make(chan struct{}), release: make(chan struct{})}
	RegisterGettingStartedResources(silentLog(), reg, tl)

	readDone := make(chan struct{})
	go func() {
		_, _, _ = reg.Read(context.Background(), "panda://getting-started", surface.MCP)
		close(readDone)
	}()
	<-tl.entered

	registerDone := make(chan struct{})
	go func() {
		reg.RegisterStatic(StaticResource{Resource: mcp.NewResource("panda://late", "late")})
		close(registerDone)
	}()

	// Give the register time to reach the write lock before the handler resumes
	// and lists resources.
	time.Sleep(150 * time.Millisecond)
	close(tl.release)

	select {
	case <-readDone:
	case <-time.After(3 * time.Second):
		t.Fatal("read did not complete while a register ran concurrently")
	}

	select {
	case <-registerDone:
	case <-time.After(time.Second):
		t.Fatal("register did not complete after the read finished")
	}
}

// Reads whose handlers list registry resources run correctly alongside a stream
// of registers.
func TestRegistryRead_ConcurrentReadsAndRegisters(t *testing.T) {
	reg := NewRegistry(silentLog())
	toolReg := &fakeToolLister{tools: []mcp.Tool{{Name: "execute_python"}, {Name: "search"}}}
	RegisterGettingStartedResources(silentLog(), reg, toolReg)

	stop := make(chan struct{})
	var writer sync.WaitGroup
	writer.Add(1)
	go func() {
		defer writer.Done()
		for i := 0; i < 200; i++ {
			select {
			case <-stop:
				return
			default:
			}
			reg.RegisterStatic(StaticResource{Resource: mcp.NewResource(fmt.Sprintf("panda://ds-%d", i), "ds")})
			time.Sleep(time.Millisecond)
		}
	}()

	var readers sync.WaitGroup
	done := make(chan struct{})
	for i := 0; i < 32; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for j := 0; j < 50; j++ {
				_, _, err := reg.Read(context.Background(), "panda://getting-started", surface.MCP)
				if err != nil {
					t.Errorf("read failed: %v", err)
					return
				}
			}
		}()
	}
	go func() {
		readers.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("concurrent reads and registers did not complete")
	}

	close(stop)
	writer.Wait()
}
