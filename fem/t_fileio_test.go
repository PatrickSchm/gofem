// Copyright 2015 Dorival Pedroso and Raul Durand. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package fem

import (
	"testing"

	"github.com/cpmech/gosl/chk"
	"github.com/cpmech/gosl/io"
)

func Test_fileio01(tst *testing.T) {

	chk.PrintTitle("fileio01")

	// start
	if !Start("data/bh16.sim", true, chk.Verbose) {
		tst.Errorf("test failed\n")
	}
	defer End()

	// domain A
	distr := false
	domA := NewDomain(Global.Sim.Regions[0], distr)
	if domA == nil {
		tst.Errorf("test failed\n")
	}
	if !domA.SetStage(0, Global.Sim.Stages[0], distr) {
		tst.Errorf("test failed\n")
	}
	for i, _ := range domA.Sol.Y {
		domA.Sol.Y[i] = float64(i)
	}
	io.Pforan("domA.Sol.Y = %v\n", domA.Sol.Y)

	// write file
	tidx := 123
	if !domA.SaveSol(tidx) {
		tst.Errorf("test failed")
		return
	}
	dir, fnk := Global.Dirout, Global.Fnkey
	io.Pfblue2("file %v written\n", out_nod_path(dir, fnk, tidx, Global.Rank))

	// domain B
	domB := NewDomain(Global.Sim.Regions[0], distr)
	if domB == nil {
		tst.Errorf("test failed\n")
	}
	if !domB.SetStage(0, Global.Sim.Stages[0], distr) {
		tst.Errorf("test failed")
	}
	io.Pfpink("domB.Sol.Y (before) = %v\n", domB.Sol.Y)

	// read file
	if !domB.ReadSol(dir, fnk, tidx) {
		tst.Errorf("test failed")
		return
	}
	io.Pfgreen("domB.Sol.Y (after) = %v\n", domB.Sol.Y)

	// check
	chk.Vector(tst, "Y", 1e-17, domA.Sol.Y, domB.Sol.Y)
	chk.Vector(tst, "dy/dt", 1e-17, domA.Sol.Dydt, domB.Sol.Dydt)
	chk.Vector(tst, "d²y/dt²", 1e-17, domA.Sol.D2ydt2, domB.Sol.D2ydt2)
}
