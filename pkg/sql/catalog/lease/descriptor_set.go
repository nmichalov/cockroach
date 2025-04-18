// Copyright 2021 The Cockroach Authors.
//
// Use of this software is governed by the CockroachDB Software License
// included in the /LICENSE file.

package lease

import (
	"bytes"
	"context"
	"fmt"
	"sort"

	"github.com/cockroachdb/cockroach/pkg/sql/catalog/descpb"
	"github.com/cockroachdb/errors"
)

// descriptorSet maintains an ordered set of descriptorVersionState objects
// sorted by version. It supports addition and removal of elements, finding the
// descriptor for a particular version, or finding the most recent version.
// The order is maintained by insert and remove and there can only be a
// unique entry for a version. Only the last two versions can be leased,
// with the last one being the latest one which is always leased.
//
// Each entry represents a time span [ModificationTime, expiration)
// and can be used by a transaction iif:
// ModificationTime <= transaction.Timestamp < expiration.
type descriptorSet struct {
	data []*descriptorVersionState
}

func (l *descriptorSet) String() string {
	var buf bytes.Buffer
	for i, s := range l.data {
		if i > 0 {
			buf.WriteString(" ")
		}
		buf.WriteString(fmt.Sprintf("%d:%d", s.GetVersion(), s.getExpiration(context.TODO()).WallTime))
	}
	return buf.String()
}

func (l *descriptorSet) insert(s *descriptorVersionState) {
	i, match := l.findIndex(s.GetVersion())
	if match {
		panic("unable to insert duplicate lease")
	}
	if i == len(l.data) {
		l.data = append(l.data, s)
		return
	}
	l.data = append(l.data, nil)
	copy(l.data[i+1:], l.data[i:])
	l.data[i] = s
}

func (l *descriptorSet) remove(s *descriptorVersionState) {
	i, match := l.findIndex(s.GetVersion())
	if !match {
		panic(errors.AssertionFailedf("can't find lease to remove: %s", s))
	}
	l.data = append(l.data[:i], l.data[i+1:]...)
}

func (l *descriptorSet) find(version descpb.DescriptorVersion) *descriptorVersionState {
	if i, match := l.findIndex(version); match {
		return l.data[i]
	}
	return nil
}

func (l *descriptorSet) findIndex(version descpb.DescriptorVersion) (int, bool) {
	i := sort.Search(len(l.data), func(i int) bool {
		s := l.data[i]
		return s.GetVersion() >= version
	})
	if i < len(l.data) {
		s := l.data[i]
		if s.GetVersion() == version {
			return i, true
		}
	}
	return i, false
}

func (l *descriptorSet) findNewest() *descriptorVersionState {
	if len(l.data) == 0 {
		return nil
	}
	return l.data[len(l.data)-1]
}

func (l *descriptorSet) findPreviousToExpire(dropped bool) *descriptorVersionState {
	// If there are no versions, then no previous version exists.
	if len(l.data) == 0 {
		return nil
	}
	// The latest version will be the previous version
	// if the descriptor is dropped.
	exp := l.data[len(l.data)-1]
	if len(l.data) > 1 && !dropped {
		// Otherwise, the second last element will be the previous version.
		exp = l.data[len(l.data)-2]
	} else if !dropped {
		// Otherwise, there is a single non-dropped element
		// avoid expiring.
		return nil
	}
	// If the refcount has hit zero then this version will be cleaned up
	// automatically. The expiration time is non-nil only if a version has
	// been expired or is considered stale.
	if exp.refcount.Load() == 0 || (exp.expiration.Load() != nil) {
		return nil
	}
	return exp
}

func (l *descriptorSet) findVersion(version descpb.DescriptorVersion) *descriptorVersionState {
	if len(l.data) == 0 {
		return nil
	}
	// Find the index of the first lease with version > targetVersion.
	i := sort.Search(len(l.data), func(i int) bool {
		return l.data[i].GetVersion() > version
	})
	if i == 0 {
		return nil
	}
	// i-1 is the index of the newest lease for the previous version (the version
	// we're looking for).
	s := l.data[i-1]
	if s.GetVersion() == version {
		return s
	}
	return nil
}
