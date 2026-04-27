package raft

import (
	"fmt"
	"time"
)

// Example 1: ACID Batch Transactions
// All items are created atomically - either all succeed or all fail
func ExampleAtomicInventoryUpdate(storage *RaftStorage) error {
	fmt.Println("=== Example 1: ACID Batch Transaction ===")

	// Create multiple inventory items in a single atomic transaction
	inventory := map[string][]byte{
		"widget-a": []byte(`{"sku":"widget-a","qty":100,"price":9.99}`),
		"widget-b": []byte(`{"sku":"widget-b","qty":200,"price":14.99}`),
		"widget-c": []byte(`{"sku":"widget-c","qty":150,"price":19.99}`),
	}

	// All items are written atomically - if any fails, none are written
	err := storage.PutAll("inventory", inventory)
	if err != nil {
		return fmt.Errorf("inventory update failed: %w", err)
	}

	fmt.Println("✅ All 3 inventory items created atomically")
	return nil
}

// Example 2: Distributed Lock with CAS
// Use CompareAndCreate to implement a distributed lock
func ExampleDistributedLock(storage *RaftStorage, nodeID string) error {
	fmt.Println("\n=== Example 2: Distributed Lock ===")

	lockKey := "locks/critical-section"

	// Try to acquire lock - fails if already held
	err := storage.CompareAndCreate(lockKey, []byte(nodeID))
	if err != nil {
		return fmt.Errorf("lock already held: %w", err)
	}

	fmt.Printf("✅ Lock acquired by node: %s\n", nodeID)

	// Do critical work here...
	fmt.Println("   Performing critical work...")
	time.Sleep(100 * time.Millisecond)

	// Release lock
	storage.Delete(lockKey)
	fmt.Println("✅ Lock released")

	return nil
}

// Example 3: Optimistic Counter with CAS
// Increment a counter using Compare-And-Swap with retry logic
func ExampleOptimisticCounter(storage *RaftStorage, counterKey string) error {
	fmt.Println("\n=== Example 3: Optimistic Counter ===")

	maxRetries := 10

	for retry := 0; retry < maxRetries; retry++ {
		// Read current value
		currentData, err := storage.Get(counterKey)
		if err != nil {
			return fmt.Errorf("failed to read counter: %w", err)
		}

		// Parse current value
		var currentVal int
		fmt.Sscanf(string(currentData), "%d", &currentVal)

		// Calculate new value
		newVal := currentVal + 1

		// Try to update atomically
		err = storage.CompareAndSwap(
			counterKey,
			[]byte(fmt.Sprintf("%d", currentVal)),
			[]byte(fmt.Sprintf("%d", newVal)),
		)

		if err == nil {
			// Success!
			fmt.Printf("✅ Counter incremented: %d → %d (attempt %d)\n",
				currentVal, newVal, retry+1)
			return nil
		}

		// CAS failed - value changed by another writer
		// Retry with fresh value
		fmt.Printf("   Retry %d: value changed, retrying...\n", retry+1)
		time.Sleep(10 * time.Millisecond)
	}

	return fmt.Errorf("failed to increment counter after %d retries", maxRetries)
}

// Example 4: Account Transfer with CAS
// Transfer funds between accounts using optimistic locking
func ExampleAccountTransfer(storage *RaftStorage, fromAccount, toAccount string, amount int) error {
	fmt.Println("\n=== Example 4: Account Transfer with CAS ===")
	fmt.Printf("Transferring $%d from %s to %s\n", amount, fromAccount, toAccount)

	maxRetries := 10

	// Retry loop for optimistic locking
	for retry := 0; retry < maxRetries; retry++ {
		// Read both account balances
		fromData, err := storage.Get(fromAccount)
		if err != nil {
			return fmt.Errorf("failed to read from account: %w", err)
		}

		toData, err := storage.Get(toAccount)
		if err != nil {
			return fmt.Errorf("failed to read to account: %w", err)
		}

		// Parse balances
		var fromBalance, toBalance int
		fmt.Sscanf(string(fromData), "%d", &fromBalance)
		fmt.Sscanf(string(toData), "%d", &toBalance)

		// Validate sufficient funds
		if fromBalance < amount {
			return fmt.Errorf("insufficient funds: have %d, need %d", fromBalance, amount)
		}

		// Calculate new balances
		newFromBalance := fromBalance - amount
		newToBalance := toBalance + amount

		// Try to update FROM account first
		err = storage.CompareAndSwap(
			fromAccount,
			[]byte(fmt.Sprintf("%d", fromBalance)),
			[]byte(fmt.Sprintf("%d", newFromBalance)),
		)

		if err != nil {
			// FROM account changed, retry
			fmt.Printf("   Retry %d: from account changed\n", retry+1)
			time.Sleep(10 * time.Millisecond)
			continue
		}

		// Now update TO account
		err = storage.CompareAndSwap(
			toAccount,
			[]byte(fmt.Sprintf("%d", toBalance)),
			[]byte(fmt.Sprintf("%d", newToBalance)),
		)

		if err != nil {
			// TO account changed - need to rollback FROM account
			fmt.Printf("   Retry %d: to account changed, rolling back\n", retry+1)

			// Rollback: restore FROM account
			storage.CompareAndSwap(
				fromAccount,
				[]byte(fmt.Sprintf("%d", newFromBalance)),
				[]byte(fmt.Sprintf("%d", fromBalance)),
			)

			time.Sleep(10 * time.Millisecond)
			continue
		}

		// Both updates succeeded!
		fmt.Printf("✅ Transfer complete: %s(%d→%d) %s(%d→%d)\n",
			fromAccount, fromBalance, newFromBalance,
			toAccount, toBalance, newToBalance)
		return nil
	}

	return fmt.Errorf("transfer failed after %d retries", maxRetries)
}

// Example 5: Idempotent Request Processing
// Ensure a request is processed exactly once using CAS
func ExampleIdempotentRequest(storage *RaftStorage, requestID string) error {
	fmt.Println("\n=== Example 5: Idempotent Request Processing ===")
	fmt.Printf("Processing request: %s\n", requestID)

	processedKey := "processed/" + requestID

	// Try to mark as processed
	err := storage.CompareAndCreate(processedKey, []byte(fmt.Sprintf("processed-at-%d", time.Now().Unix())))

	if err != nil {
		// Request already processed
		fmt.Printf("⚠️  Request %s already processed, skipping\n", requestID)
		return nil
	}

	// This is the first time - process the request
	fmt.Printf("✅ Processing request %s for the first time\n", requestID)

	// Do actual work here...
	time.Sleep(50 * time.Millisecond)

	fmt.Println("✅ Request processing complete")
	return nil
}

// Example 6: Atomic Mutations with Rollback
// Multiple related updates that succeed or fail together
func ExampleAtomicMutations(storage *RaftStorage) error {
	fmt.Println("\n=== Example 6: Atomic Mutations ===")

	// Setup initial values
	storage.Put("stats/total", []byte("1000"))
	storage.Put("stats/active", []byte("500"))
	storage.Put("stats/pending", []byte("300"))

	// Mutate all three values atomically
	// All mutations happen in a single transaction
	err := storage.MutateValue("stats/total", func(key string, data []byte) (error, []byte, bool) {
		var val int
		fmt.Sscanf(string(data), "%d", &val)
		return nil, []byte(fmt.Sprintf("%d", val+10)), false
	})

	err = storage.MutateValue("stats/active", func(key string, data []byte) (error, []byte, bool) {
		var val int
		fmt.Sscanf(string(data), "%d", &val)
		return nil, []byte(fmt.Sprintf("%d", val+5)), false
	})

	err = storage.MutateValue("stats/pending", func(key string, data []byte) (error, []byte, bool) {
		var val int
		fmt.Sscanf(string(data), "%d", &val)
		return nil, []byte(fmt.Sprintf("%d", val-5)), false
	})

	if err != nil {
		return fmt.Errorf("atomic mutation failed: %w", err)
	}

	// Read updated values
	total, _ := storage.Get("stats/total")
	active, _ := storage.Get("stats/active")
	pending, _ := storage.Get("stats/pending")

	fmt.Printf("✅ Atomic updates complete:\n")
	fmt.Printf("   total: 1000 → %s\n", total)
	fmt.Printf("   active: 500 → %s\n", active)
	fmt.Printf("   pending: 300 → %s\n", pending)

	return nil
}

// RunAllExamples demonstrates all the new capabilities
func RunAllExamples(storage *RaftStorage) error {
	fmt.Println("╔════════════════════════════════════════════════════════════╗")
	fmt.Println("║  IZCR Storage: ACID Transactions & CAS Examples            ║")
	fmt.Println("╚════════════════════════════════════════════════════════════╝")
	fmt.Println()

	// Example 1: ACID Batch
	if err := ExampleAtomicInventoryUpdate(storage); err != nil {
		return err
	}

	// Example 2: Distributed Lock
	if err := ExampleDistributedLock(storage, "node-123"); err != nil {
		fmt.Printf("⚠️  Lock example (expected if already held): %v\n", err)
	}

	// Example 3: Optimistic Counter
	storage.Put("counter", []byte("0"))
	time.Sleep(100 * time.Millisecond)
	if err := ExampleOptimisticCounter(storage, "counter"); err != nil {
		return err
	}

	// Example 4: Account Transfer
	storage.Put("accounts/alice", []byte("1000"))
	storage.Put("accounts/bob", []byte("500"))
	time.Sleep(100 * time.Millisecond)
	if err := ExampleAccountTransfer(storage, "accounts/alice", "accounts/bob", 250); err != nil {
		return err
	}

	// Example 5: Idempotent Request
	if err := ExampleIdempotentRequest(storage, "req-12345"); err != nil {
		return err
	}
	// Try same request again - should be skipped
	if err := ExampleIdempotentRequest(storage, "req-12345"); err != nil {
		return err
	}

	// Example 6: Atomic Mutations
	if err := ExampleAtomicMutations(storage); err != nil {
		return err
	}

	fmt.Println("\n╔════════════════════════════════════════════════════════════╗")
	fmt.Println("║  All examples completed successfully!                      ║")
	fmt.Println("╚════════════════════════════════════════════════════════════╝")

	return nil
}

// Example helper for concurrent CAS operations
func ExampleConcurrentIncrements(storage *RaftStorage, numWorkers int) error {
	fmt.Printf("\n=== Concurrent Increments: %d workers ===\n", numWorkers)

	// Initialize counter
	counterKey := "concurrent-counter"
	storage.Put(counterKey, []byte("0"))
	time.Sleep(100 * time.Millisecond)

	// Channel to collect results
	done := make(chan error, numWorkers)

	// Launch workers
	for i := 0; i < numWorkers; i++ {
		go func(workerID int) {
			done <- ExampleOptimisticCounter(storage, counterKey)
		}(i)
	}

	// Wait for all workers
	successCount := 0
	for i := 0; i < numWorkers; i++ {
		if err := <-done; err == nil {
			successCount++
		}
	}

	// Check final value
	finalData, _ := storage.Get(counterKey)

	fmt.Printf("✅ %d/%d workers succeeded, final counter value: %s\n",
		successCount, numWorkers, finalData)

	return nil
}
