package evm

import (
	"os/exec"
	"testing"
)

func TestPythonBytecodeHelpersEIP8024(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}

	script := `
import importlib.util
import sys
import types

pkg = types.ModuleType("ethpandaops")
pkg.__path__ = []
pkg._runtime = types.ModuleType("ethpandaops._runtime")

ethnode = types.ModuleType("ethpandaops.ethnode")
ethnode.execution_rpc = lambda *args, **kwargs: None

sys.modules["ethpandaops"] = pkg
sys.modules["ethpandaops._runtime"] = pkg._runtime
sys.modules["ethpandaops.ethnode"] = ethnode

spec = importlib.util.spec_from_file_location("evm_under_test", "python/evm.py")
evm = importlib.util.module_from_spec(spec)
spec.loader.exec_module(evm)

def check(got, want):
    if got != want:
        raise AssertionError(f"got {got!r}, want {want!r}")

check(evm.assemble(["DUPN", 17]), "0xe680")
check(evm.assemble(["SWAPN", 108]), "0xe7db")
check(evm.assemble(["EXCHANGE", 2, 3]), "0xe89d")
check(evm.assemble(["EXCHANGE", 1, 19]), "0xe82f")

check(evm.disassemble("0xe680"), [{"pc": 0, "op": "DUPN", "immediate": "0x80", "operand": 17}])
check(evm.disassemble("0xe7db"), [{"pc": 0, "op": "SWAPN", "immediate": "0xdb", "operand": 108}])
check(evm.disassemble("0xe89d"), [{"pc": 0, "op": "EXCHANGE", "immediate": "0x9d", "operands": [2, 3]}])
check(evm.disassemble("0xe75b"), [{"pc": 0, "op": "INVALID_SWAPN"}, {"pc": 1, "op": "JUMPDEST"}])
check(evm.disassemble("0xe6605b"), [{"pc": 0, "op": "INVALID_DUPN"}, {"pc": 1, "op": "PUSH1", "operand": "0x5b"}])
check(evm.disassemble("0xe852"), [{"pc": 0, "op": "INVALID_EXCHANGE"}, {"pc": 1, "op": "MSTORE"}])
`

	cmd := exec.Command("python3", "-c", script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("python bytecode helper smoke failed: %v\n%s", err, out)
	}
}
