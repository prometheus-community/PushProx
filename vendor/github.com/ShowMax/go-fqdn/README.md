# go-fqdn
Simple wrapper around `net` and `os` golang standard libraries providing Fully Qualified Domain Name of the machine.

## Usage
Get the library...
```
$ go get github.com/ShowMax/go-fqdn
```
...and write some code.
```
package main

import (
	"fmt"
	"github.com/ShowMax/go-fqdn"
)

func main() {
	fmt.Println(fqdn.Get())
}
```

`fqdn.Get()` returns:
- machine's FQDN if found.
- hostname if FQDN is not found.
- return "unknown" if nothing is found.
