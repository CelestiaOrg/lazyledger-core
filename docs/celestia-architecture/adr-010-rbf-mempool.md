# ADR 010: RBF mempool

## Changelog

- 2024-08.10.24: Initial draft (@ninabarbakadze)

## Context

A key aspect of user experience for any blockchain system, including Celestia, is the smooth and reliable transaction submission. One of the critical components in achieving this is a mempool that not only handles high tx volume gracefully but also manages transaction ordering effectively to minimize unnecessary rejections. The current mempool implementations priority and CAT prioritize transactions solely based on fees without considering nonce order. Lack of nonce awareness in mempools can result in otherwise valid transactions being rejected with nonce errors due to a higher nonce transaction being prioritized while a lower nonce transaction is still pending.

## Decision

Extend the current mempool implementation to be nonce aware.

## Detailed Design

When a new transaction is added to the mempool, the ABCI `CheckTx()` method validates it against a predefined set of conditions in the state machine. To support RBF, we first need to extend the `CheckTx()` return type `ResponseCheckTx` to include the Nonce field alongside the `Sender` and transaction `Priority`. To implement this, we can either extend an existing `AnteHandler` decorator or add a new one that sets both the Sender and Nonce as part of the `CheckTx()` context. After all decorators are executed, `ctx.Nonce()` and `ctx.Sender()` can be set on `ResponseCheckTX` and returned back to the consensus node.

After a transaction successfully passes the `CheckTx()` validation, it is added to the `txmp.store` via `addNewTransaction()`. Here, it is stored as a `WrappedTx` type, which sets fields like `priority` and `sender` for the transactions in the mempool. By extending the `WrappedTx` type to include the `nonce`, we can start tracking nonces and ensure that they are accounted for during transaction prioritization. Currently, the sender field is not set in the priority mempool, as it prevents a single sender from submitting multiple transactions. However, since we plan on  fully switching to the CAT pool, we can bypass this constraint.

For RBF functionality, when a new transaction is submitted, we will check whether the user has an existing transaction pending with the same nonce. If so, we will compare the priorities of both transactions and the transaction with the lower priority will be discarded, while the higher-priority one will be inserted into the mempool. Additionally, we will ensure that this process reshuffles the global priority of transactions accordingly.

Currently, all transactions are sorted by priority in the `allEntriesSorted()` function. To support nonce-based prioritization, we will update this function to prioritize nonces when ordering transactions. This way, nonce will take precedence when sorting high-fee transactions.

These changes should not be breaking for either celestia-app or celestia-core. The necessary changes involve extending `ResponseCheckTX` in core first, then extending the `CheckTx()` response in the state machine to include both the sender and nonce. Once this is complete, we can implement RBF in celestia-core, release the upgrade, and bump celestia-core version in celestia-app.

Unit tests will be written for both celestia-core and celestia-app to ensure functionality and stability. Additionally, manual testing on internal testnets will be conducted to validate the changes.

While these updates will result in slightly more disk writing, memory usage and overall efficiency should remain unaffected.

## Status

Proposed

## Consequences

### Positive

**Improved Transaction Fee Efficiency:** Users can dynamically adjust fees to prioritize their transactions, ensuring faster processing when needed.

**Better Nonce Management:** The system enforces proper nonce order, preventing high-fee transactions from being prioritized out of sequence.

### Negative

Users can keep increasing the fee and cause excessive transaction resending.

## References

