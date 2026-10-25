package pc

import "errors"

const lockFileName = "splitter.lock"

// errAnotherInstance means another app holds the instance lock. Two instances
// would kill each other's children at pre-flight (CleanupStale) and fight over
// the TUN and the Clash API port.
var errAnotherInstance = errors.New("another splitter instance is already running")
