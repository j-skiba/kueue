/*
Copyright The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
Distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package resources

// LazyRequests wraps a base ResourceRequests (map or slice)
// and performs copy-on-write (lazy cloning) when mutations occur.
type LazyRequests struct {
	base      ResourceRequests
	cached    ResourceRequests
	hasBase   bool
	hasCached bool
}

func NewLazyRequests(base ResourceRequests) LazyRequests {
	l := LazyRequests{base: base}
	if base != nil {
		if req, ok := base.(Requests); ok {
			l.hasBase = req != nil
		} else if sr, ok := base.(*SliceRequests); ok {
			l.hasBase = sr != nil
		} else {
			l.hasBase = true
		}
	}
	return l
}

// IsValid returns true if either the base or cached resource collection is initialized.
func (l *LazyRequests) IsValid() bool {
	return l.hasBase || l.hasCached
}

// Get returns the underlying ResourceRequests (either the cached clone if mutated, or base).
func (l *LazyRequests) Get() ResourceRequests {
	if l.hasCached {
		return l.cached
	}
	return l.base
}

// Sub subtracts subRequests from the underlying ResourceRequests,
// cloning base on first write.
func (l *LazyRequests) Sub(subRequests ResourceRequests) {
	if subRequests == nil || subRequests.IsEmpty() {
		return
	}
	if !l.hasCached {
		if l.hasBase {
			l.cached = l.base.CloneResourceRequests()
		} else {
			l.cached = subRequests.CreateEmpty()
		}
		l.hasCached = true
	}
	if l.cached != nil {
		l.cached.Sub(subRequests)
	}
}

// Add adds addRequests to the underlying ResourceRequests,
// cloning base on first write.
func (l *LazyRequests) Add(addRequests ResourceRequests) {
	if addRequests == nil || addRequests.IsEmpty() {
		return
	}
	if !l.hasCached {
		if l.hasBase {
			l.cached = l.base.CloneResourceRequests()
		} else {
			l.cached = addRequests.CreateEmpty()
		}
		l.hasCached = true
	}
	if l.cached != nil {
		l.cached.Add(addRequests)
	}
}
