package txn

import "github.com/Choaterboater/pika/internal/fsutil"

// Durability helpers delegate to the shared fsutil package so txn and
// evidence run one implementation of the per-platform fsync contract
// (unix real fsyncs; Windows best-effort read-mode tolerance).

func syncFile(path string) error { return fsutil.SyncFile(path) }

func syncDir(dir string) error { return fsutil.SyncDir(dir) }

func existingAncestor(dir string) string { return fsutil.ExistingAncestor(dir) }

func syncCreatedChain(dir, anchor string) error { return fsutil.SyncCreatedChain(dir, anchor) }
