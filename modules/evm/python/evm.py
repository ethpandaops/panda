"""EVM execution, tracing, transaction submission, and bytecode utilities.

All execution runs on the target devnet node — fork rules are always correct.
Compose with ethnode.execution_rpc for anything not covered here.
"""

from __future__ import annotations

import json
import os
from typing import Any

from ethpandaops import _runtime
from ethpandaops.ethnode import execution_rpc as _rpc

# ---------------------------------------------------------------------------
# Opcode table
# ---------------------------------------------------------------------------

_OPS: dict[str, int] = {
    "STOP": 0x00, "ADD": 0x01, "MUL": 0x02, "SUB": 0x03, "DIV": 0x04,
    "SDIV": 0x05, "MOD": 0x06, "SMOD": 0x07, "ADDMOD": 0x08, "MULMOD": 0x09,
    "EXP": 0x0A, "SIGNEXTEND": 0x0B,
    "LT": 0x10, "GT": 0x11, "SLT": 0x12, "SGT": 0x13, "EQ": 0x14,
    "ISZERO": 0x15, "AND": 0x16, "OR": 0x17, "XOR": 0x18, "NOT": 0x19,
    "BYTE": 0x1A, "SHL": 0x1B, "SHR": 0x1C, "SAR": 0x1D,
    "KECCAK256": 0x20, "SHA3": 0x20,
    "ADDRESS": 0x30, "BALANCE": 0x31, "ORIGIN": 0x32, "CALLER": 0x33,
    "CALLVALUE": 0x34, "CALLDATALOAD": 0x35, "CALLDATASIZE": 0x36,
    "CALLDATACOPY": 0x37, "CODESIZE": 0x38, "CODECOPY": 0x39,
    "GASPRICE": 0x3A, "EXTCODESIZE": 0x3B, "EXTCODECOPY": 0x3C,
    "RETURNDATASIZE": 0x3D, "RETURNDATACOPY": 0x3E, "EXTCODEHASH": 0x3F,
    "BLOCKHASH": 0x40, "COINBASE": 0x41, "TIMESTAMP": 0x42, "NUMBER": 0x43,
    "PREVRANDAO": 0x44, "DIFFICULTY": 0x44, "GASLIMIT": 0x45, "CHAINID": 0x46,
    "SELFBALANCE": 0x47, "BASEFEE": 0x48, "BLOBHASH": 0x49, "BLOBBASEFEE": 0x4A,
    "POP": 0x50, "MLOAD": 0x51, "MSTORE": 0x52, "MSTORE8": 0x53,
    "SLOAD": 0x54, "SSTORE": 0x55, "JUMP": 0x56, "JUMPI": 0x57,
    "PC": 0x58, "MSIZE": 0x59, "GAS": 0x5A, "JUMPDEST": 0x5B,
    "TLOAD": 0x5C, "TSTORE": 0x5D, "MCOPY": 0x5E, "PUSH0": 0x5F,
    # EOF (Fusaka) opcodes
    "DUPN": 0xE6, "SWAPN": 0xE7, "EXCHANGE": 0xE8,
    # Calls and create
    "CREATE": 0xF0, "CALL": 0xF1, "CALLCODE": 0xF2, "RETURN": 0xF3,
    "DELEGATECALL": 0xF4, "CREATE2": 0xF5, "STATICCALL": 0xFA,
    "REVERT": 0xFD, "INVALID": 0xFE, "SELFDESTRUCT": 0xFF,
}

# PUSH1..PUSH32 and DUP1..DUP16, SWAP1..SWAP16, LOG0..LOG4
_OPS.update({f"PUSH{i}": 0x5F + i for i in range(1, 33)})
_OPS.update({f"DUP{i}": 0x7F + i for i in range(1, 17)})
_OPS.update({f"SWAP{i}": 0x8F + i for i in range(1, 17)})
_OPS.update({f"LOG{i}": 0xA0 + i for i in range(5)})

_OPS_BY_BYTE: dict[int, str] = {}
for _name, _byte in _OPS.items():
    _OPS_BY_BYTE.setdefault(_byte, _name)  # first name wins (e.g. KECCAK256 over SHA3)

_FAUCETS: dict[str, str] = json.loads(os.environ.get("ETHPANDAOPS_EVM_FAUCET_NETWORKS", "{}"))


def _require_available() -> None:
    if not os.environ.get("ETHPANDAOPS_EVM_AVAILABLE", "").strip():
        raise ValueError("EVM module is not enabled; ethnode datasource is required.")


# ---------------------------------------------------------------------------
# Bytecode utilities
# ---------------------------------------------------------------------------

def assemble(ops: list[str | int]) -> str:
    """Assemble opcode names and immediate bytes into hex bytecode.

    ops may mix opcode name strings and integer immediates.
    Multi-byte integers are big-endian encoded to their minimal byte length.

    Example:
        assemble(["PUSH1", 0x01, "PUSH1", 0x01, "ADD", "STOP"]) -> "0x60016001010"
    """
    out: list[int] = []

    for item in ops:
        if isinstance(item, str):
            byte = _OPS.get(item.upper())
            if byte is None:
                raise ValueError(f"Unknown opcode: {item!r}")
            out.append(byte)
        elif isinstance(item, int):
            if 0 <= item <= 0xFF:
                out.append(item)
            else:
                length = (item.bit_length() + 7) // 8
                out.extend(item.to_bytes(length, "big"))
        else:
            raise TypeError(f"Expected str or int, got {type(item).__name__!r}")

    return "0x" + bytes(out).hex()


def disassemble(bytecode: str) -> list[dict[str, Any]]:
    """Disassemble hex bytecode into [{pc, op, operand}] dicts.

    PUSH immediates are captured in 'operand' as a hex string.
    """
    data = bytes.fromhex(bytecode.removeprefix("0x"))
    result: list[dict[str, Any]] = []
    i = 0

    while i < len(data):
        byte = data[i]
        name = _OPS_BY_BYTE.get(byte, f"0x{byte:02x}")
        entry: dict[str, Any] = {"pc": i, "op": name}

        push_size = (byte - 0x5F) if 0x60 <= byte <= 0x7F else 0
        if push_size:
            operand = data[i + 1: i + 1 + push_size]
            entry["operand"] = "0x" + operand.hex()
            i += push_size

        result.append(entry)
        i += 1

    return result


# ---------------------------------------------------------------------------
# EVM execution against devnet nodes
# ---------------------------------------------------------------------------

def call(
    network: str,
    instance: str,
    data: str,
    to: str | None = None,
    from_: str | None = None,
    value: int = 0,
    gas: int | None = None,
    block: str = "latest",
) -> str:
    """Execute bytecode or calldata; returns the raw hex result.

    to=None runs data as init code (contract creation context) via eth_call.
    Compose with disassemble() to inspect the returned bytecode.
    """
    _require_available()
    params: dict[str, Any] = {"data": data, "value": hex(value)}
    if to is not None:
        params["to"] = to
    if from_ is not None:
        params["from"] = from_
    if gas is not None:
        params["gas"] = hex(gas)
    return _rpc(network, instance, "eth_call", [params, block]) or "0x"


def trace(
    network: str,
    instance: str,
    data: str,
    to: str | None = None,
    from_: str | None = None,
    gas: int | None = None,
    block: str = "latest",
) -> list[dict[str, Any]]:
    """Trace execution opcode-by-opcode via debug_traceCall.

    Returns structLogs: [{op, pc, gas, gasCost, stack, memory, storage, depth}].
    to=None traces init code execution (contract creation context).
    Useful for pre-deployment opcode trials and gas cost analysis.
    """
    _require_available()
    tx_params: dict[str, Any] = {
        "data": data,
        "value": "0x0",
        "gas": hex(gas) if gas is not None else "0x100000",
    }
    if to is not None:
        tx_params["to"] = to
    if from_ is not None:
        tx_params["from"] = from_

    raw = _rpc(network, instance, "debug_traceCall", [
        tx_params,
        block,
        {"enableMemory": True, "enableStack": True, "disableStorage": False},
    ])
    return raw.get("structLogs", []) if isinstance(raw, dict) else []


def trace_tx(
    network: str,
    instance: str,
    txhash: str,
) -> list[dict[str, Any]]:
    """Trace an already-mined transaction via debug_traceTransaction.

    Same structLog format as trace(). Use for post-deployment triage:
    fetch the trace from each client, zip and diff to locate the diverging step.
    """
    _require_available()
    raw = _rpc(network, instance, "debug_traceTransaction", [
        txhash,
        {"enableMemory": True, "enableStack": True, "disableStorage": False},
    ])
    return raw.get("structLogs", []) if isinstance(raw, dict) else []


# ---------------------------------------------------------------------------
# Transaction submission
# ---------------------------------------------------------------------------

def tx(
    network: str,
    instance: str,
    private_key: str,
    to: str | None,
    data: str = "0x",
    value: int = 0,
    gas: int | None = None,
) -> str:
    """Sign and submit an EIP-1559 transaction; returns the tx hash.

    to=None deploys a contract (data is treated as init code).
    gas=None estimates automatically via eth_estimateGas.
    """
    _require_available()
    from eth_account import Account  # noqa: PLC0415

    acct = Account.from_key(private_key)
    address = acct.address

    chain_id_hex = _rpc(network, instance, "eth_chainId", [])
    nonce_hex = _rpc(network, instance, "eth_getTransactionCount", [address, "pending"])
    fee_history = _rpc(network, instance, "eth_feeHistory", ["0x1", "latest", []]) or {}

    base_fees = fee_history.get("baseFeePerGas", [])
    base_fee = int(base_fees[-1], 16) if base_fees else 1_000_000_000
    priority_fee = 1_000_000_000  # 1 gwei

    tx_params: dict[str, Any] = {
        "type": 2,
        "chainId": int(chain_id_hex, 16),
        "nonce": int(nonce_hex, 16),
        "maxFeePerGas": base_fee * 2 + priority_fee,
        "maxPriorityFeePerGas": priority_fee,
        "value": value,
        "data": data,
    }
    if to is not None:
        tx_params["to"] = to

    if gas is None:
        est_params: dict[str, Any] = {"from": address, "data": data, "value": hex(value)}
        if to is not None:
            est_params["to"] = to
        estimated_hex = _rpc(network, instance, "eth_estimateGas", [est_params])
        tx_params["gas"] = int(int(estimated_hex, 16) * 1.2)
    else:
        tx_params["gas"] = gas

    signed = acct.sign_transaction(tx_params)
    return _rpc(network, instance, "eth_sendRawTransaction", [
        "0x" + signed.raw_transaction.hex()
    ])


# ---------------------------------------------------------------------------
# Wallet
# ---------------------------------------------------------------------------

def wallet(private_key: str | None = None) -> dict[str, str]:
    """Generate a new keypair or derive address from an existing private key.

    Returns {address, private_key}. The private key is a 0x-prefixed hex string.
    Pass to evm.tx() and evm.faucet() to fund and submit transactions.
    """
    from eth_account import Account  # noqa: PLC0415

    acct = Account.from_key(private_key) if private_key is not None else Account.create()
    return {"address": acct.address, "private_key": "0x" + acct.key.hex()}


# ---------------------------------------------------------------------------
# Faucet
# ---------------------------------------------------------------------------

def faucet(network: str, address: str) -> str:
    """Return the PoW faucet URL for the network pre-filled with the given address.

    Open this URL in a browser, complete the PoW challenge, and the address
    receives test ETH. Source: https://github.com/pk910/PoWFaucet
    """
    url = _FAUCETS.get(network)
    if not url:
        available = list(_FAUCETS.keys())
        raise ValueError(
            f"No faucet known for network {network!r}. Available: {available or 'none'}"
        )
    sep = "&" if "?" in url else "?"
    return f"{url}{sep}address={address}"
