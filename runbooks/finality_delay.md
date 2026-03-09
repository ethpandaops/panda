---
name: Investigate Finality Delay
description: Systematic diagnosis of network finality issues when epochs are not finalizing
tags: [finality, consensus, attestations, incident, epoch, validators]
prerequisites: [xatu, xatu-cbt, prometheus, dora]
---

When the network isn't finalizing, you MUST verify current status before deep diving - the issue MAY have self-resolved.

## Approach

1. **Check current status first** - Use Dora's network overview. This SHOULD be your starting point since it's fast and authoritative.

   ```python
   from ethpandaops import dora
   overview = dora.get_network_overview("mainnet")
   epochs_behind = overview["current_epoch"] - overview.get("finalized_epoch", 0)
   print(f"Epochs behind: {epochs_behind}")
   print(f"Current epoch: {overview['current_epoch']}")
   print(f"Finalized epoch: {overview.get('finalized_epoch', 'N/A')}")
   ```

2. **Query attestation participation** - If finality is delayed >2 epochs, check participation rates. You MUST use the xatu-cbt cluster for participation queries (pre-aggregated, faster). Use `search(type="examples", query="attestation participation")` for the query pattern.

   Key metrics to check:
   - Target participation rate (should be >66.7% for finality)
   - Source participation rate
   - Head participation rate

3. **Check proposer health** - You MAY skip this if participation looks normal. Look for patterns in missed slots using `search(type="examples", query="missed slots")`.

   Questions to answer:
   - Are specific validators missing proposals?
   - Is there geographic correlation in missed slots?
   - Did missed slots spike at a particular time?

4. **Identify problematic validators** - If participation is low, find which validators are underperforming. Use `search(type="examples", query="validator performance")` for query patterns.

5. **Check for client bugs** - You SHOULD check if issues correlate with specific client types. Use `search(type="examples", query="client distribution")` to understand the client mix.

6. **Generate Dora links** - Always provide the user with Dora deep links so they can explore further in the UI. Use `search(type="examples", query="dora")` for link generation patterns.

## Key Thresholds

- Finality requires >66.7% (2/3) of stake attesting correctly
- Normal finality lag is 2 epochs (~13 minutes on mainnet)
- >4 epochs without finality is cause for concern
- >8 epochs suggests a significant network issue
