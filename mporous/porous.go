// Copyright 2015 Dorival Pedroso and Raul Durand. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// package mporous implements models for porous media based on the Theory of Porous Media
//  References:
//   [1] Pedroso DM (2015) A consistent u-p formulation for porous media with hysteresis.
//       Int Journal for Numerical Methods in Engineering, 101(8) 606-634
//       http://dx.doi.org/10.1002/nme.4808
//   [2] Pedroso DM (2015) A solution to transient seepage in unsaturated porous media.
//       Computer Methods in Applied Mechanics and Engineering, 285 791-816
//       http://dx.doi.org/10.1016/j.cma.2014.12.009
package mporous

import (
	"log"
	"math"

	"github.com/cpmech/gofem/mconduct"
	"github.com/cpmech/gofem/mreten"
	"github.com/cpmech/gosl/chk"
	"github.com/cpmech/gosl/fun"
	"github.com/cpmech/gosl/io"
	"github.com/cpmech/gosl/utl"
)

// Model holds material parameters for porous media
//  References:
//   [1] Pedroso DM (2015) A consistent u-p formulation for porous media with hysteresis.
//       Int Journal for Numerical Methods in Engineering, 101(8) 606-634
//       http://dx.doi.org/10.1002/nme.4808
//   [2] Pedroso DM (2015) A solution to transient seepage in unsaturated porous media.
//       Computer Methods in Applied Mechanics and Engineering, 285 791-816
//       http://dx.doi.org/10.1016/j.cma.2014.12.009
type Model struct {

	// constants
	NmaxIt  int     // max number iterations in Update
	Itol    float64 // iterations tolerance in Update
	PcZero  float64 // minimum value allowed for pc
	MEtrial bool    // perform Modified-Euler trial to start update process
	ShowR   bool    // show residual values in Update
	AllBE   bool    // use BE for all models, including those that directly implements sl=f(pc)
	Ncns    bool    // use non-consistent method for all derivatives (see [1])
	Ncns2   bool    // use non-consistent method only for second order derivatives (see [1])

	// parameters
	Nf0   float64 // nf0: initial volume fraction of all fluids ~ porosity
	RhoL0 float64 // ρL0: initial liquid real density
	RhoG0 float64 // ρG0: initial gas real density
	RhoS0 float64 // real (intrinsic) density of solids
	BulkL float64 // liquid bulk moduli at temperature θini
	RTg   float64 // R*Θ*g: initial gas constant
	Gref  float64 // reference gravity, at time of measuring ksat, kgas
	Pkl   float64 // isotrpic liquid saturated conductivity
	Pkg   float64 // isotrpic gas saturated conductivity

	// derived
	Cl    float64     // liquid compresssibility
	Cg    float64     // gas compressibility
	Klsat [][]float64 // klsat ÷ Gref
	Kgsat [][]float64 // kgsat ÷ Gref

	// conductivity and retention models
	Cnd mconduct.Model // liquid-gas conductivity models
	Lrm mreten.Model   // retention model

	// auxiliary
	nonrateLrm mreten.Nonrate // LRM is of non-rate type
}

// Init initialises this structure
func (o *Model) Init(prms fun.Prms, cnd mconduct.Model, lrm mreten.Model) (err error) {

	// conductivity and retention models
	if cnd == nil || lrm == nil {
		return chk.Err("mporous.Init: conductivity and liquid retention models must be non nil. cnd=%v, lrm=%v\n", cnd, lrm)
	}
	o.Cnd = cnd
	o.Lrm = lrm
	if m, ok := o.Lrm.(mreten.Nonrate); ok {
		o.nonrateLrm = m
	}

	// constants
	o.NmaxIt = 20
	o.Itol = 1e-9
	o.PcZero = 1e-10
	o.MEtrial = true

	// saturated conductivities
	var klx, kly, klz float64
	var kgx, kgy, kgz float64

	// read paramaters in
	o.RTg = 1.0
	for _, p := range prms {
		switch p.N {
		case "NmaxIt":
			o.NmaxIt = int(p.V)
		case "Itol":
			o.Itol = p.V
		case "PcZero":
			o.PcZero = p.V
		case "MEtrial":
			o.MEtrial = p.V > 0
		case "ShowR":
			o.ShowR = p.V > 0
		case "AllBE":
			o.AllBE = p.V > 0
		case "Ncns":
			o.Ncns = p.V > 0
		case "Ncns2":
			o.Ncns2 = p.V > 0
		case "nf0":
			o.Nf0 = p.V
		case "RhoL0":
			o.RhoL0 = p.V
		case "RhoG0":
			o.RhoG0 = p.V
		case "RhoS0":
			o.RhoS0 = p.V
		case "BulkL":
			o.BulkL = p.V
		case "RTg":
			o.RTg = p.V
		case "gref":
			o.Gref = p.V
		case "kl":
			o.Pkl = p.V
			klx, kly, klz = p.V, p.V, p.V
		case "kg":
			o.Pkg = p.V
			kgx, kgy, kgz = p.V, p.V, p.V
		default:
			return chk.Err("mporous.Model: parameter named %q is incorrect\n", p.N)
		}
	}

	// derived
	o.Cl = o.RhoL0 / o.BulkL
	o.Cg = 1.0 / o.RTg
	o.Klsat = [][]float64{
		{klx / o.Gref, 0, 0},
		{0, kly / o.Gref, 0},
		{0, 0, klz / o.Gref},
	}
	o.Kgsat = [][]float64{
		{kgx / o.Gref, 0, 0},
		{0, kgy / o.Gref, 0},
		{0, 0, kgz / o.Gref},
	}
	return
}

// GetPrms gets (an example) of parameters
func (o Model) GetPrms(example bool) fun.Prms {
	if example {
		return fun.Prms{
			&fun.Prm{N: "nf0", V: 0.3},
			&fun.Prm{N: "RhoL0", V: 1},
			&fun.Prm{N: "RhoG0", V: 0.01},
			&fun.Prm{N: "RhoS0", V: 2.7},
			&fun.Prm{N: "BulkL", V: 2.2e6},
			&fun.Prm{N: "RTg", V: 0.02},
			&fun.Prm{N: "gref", V: 10},
			&fun.Prm{N: "kl", V: 1e-3},
			&fun.Prm{N: "kg", V: 1e-2},
		}
	}
	return fun.Prms{
		&fun.Prm{N: "nf0", V: o.Nf0},
		&fun.Prm{N: "RhoL0", V: o.RhoL0},
		&fun.Prm{N: "RhoG0", V: o.RhoG0},
		&fun.Prm{N: "RhoS0", V: o.RhoS0},
		&fun.Prm{N: "BulkL", V: o.BulkL},
		&fun.Prm{N: "RTg", V: o.RTg},
		&fun.Prm{N: "gref", V: o.Gref},
		&fun.Prm{N: "kl", V: o.Pkl},
		&fun.Prm{N: "kg", V: o.Pkg},
	}
}

// NewState creates and initialises a new state structure
//  Note: returns nil on errors
func (o Model) NewState(ρL, ρG, pl, pg float64) (s *State, err error) {
	sl := 1.0
	pc := pg - pl
	if pc > 0 {
		sl, err = mreten.Update(o.Lrm, 0, 1, pc)
		if err != nil {
			return
		}
	}
	ns0 := 1.0 - o.Nf0
	s = &State{ns0, sl, ρL, ρG, 0, false}
	return
}

// Update updates state
//  pl and pg are updated (new) values
func (o Model) Update(s *State, Δpl, Δpg, pl, pg float64) (err error) {

	// auxiliary variables
	slmin := o.Lrm.SlMin()
	Δpc := Δpg - Δpl
	wet := Δpc < 0
	pl0 := pl - Δpl
	pg0 := pg - Δpg
	pc0 := pg0 - pl0
	sl0 := s.A_sl
	pc := pc0 + Δpc
	sl := sl0

	// update liquid saturation
	if pc <= 0.0 {
		sl = 1 // full liquid saturation if capillary pressure is ineffective

	} else if o.nonrateLrm != nil && !o.AllBE {
		sl = o.nonrateLrm.Sl(pc) // handle simple retention models

	} else { // unsaturated case with rate-type model

		// trial liquid saturation update
		fA, e := o.Lrm.Cc(pc0, sl0, wet)
		if e != nil {
			return e
		}
		if o.MEtrial {
			slFE := sl0 + Δpc*fA
			fB, e := o.Lrm.Cc(pc, slFE, wet)
			if e != nil {
				return e
			}
			sl += 0.5 * Δpc * (fA + fB)
		} else {
			sl += Δpc * fA
		}

		// fix trial sl out-of-range values
		if sl < slmin {
			sl = slmin
		}
		if sl > 1 {
			sl = 1
		}

		// message
		if o.ShowR {
			io.PfYel("%6s%18s%18s%18s%18s%8s\n", "it", "Cc", "sl", "δsl", "r", "ex(r)")
		}

		// backward-Euler update
		var f, r, J, δsl float64
		var it int
		for it = 0; it < o.NmaxIt; it++ {
			f, err = o.Lrm.Cc(pc, sl, wet)
			if err != nil {
				return
			}
			r = sl - sl0 - Δpc*f
			if o.ShowR {
				io.Pfyel("%6d%18.14f%18.14f%18.14f%18.10e%8d\n", it, f, sl, δsl, r, utl.Expon(r))
			}
			if math.Abs(r) < o.Itol {
				break
			}
			J, err = o.Lrm.J(pc, sl, wet)
			if err != nil {
				return
			}
			δsl = -r / (1.0 - Δpc*J)
			sl += δsl
			if math.IsNaN(sl) {
				return chk.Err("NaN found: Δpc=%v f=%v r=%v J=%v sl=%v\n", Δpc, f, r, J, sl)
			}
		}

		// message
		if o.ShowR {
			io.Pfgrey("  pc0=%.6f  sl0=%.6f  Δpl=%.6f  Δpg=%.6f  Δpc=%.6f\n", pc0, sl0, Δpl, Δpg, Δpc)
			io.Pfgrey("  converged with %d iterations\n", it)
		}

		// check convergence
		if it == o.NmaxIt {
			return chk.Err("saturation update failed after %d iterations\n", it)
		}
	}

	// check results
	if pc < 0 && sl < 1 {
		return chk.Err("inconsistent results: saturation must be equal to one when the capillary pressure is ineffective. pc = %g < 0 and sl = %g < 1 is incorrect", pc, sl)
	}
	if sl < slmin {
		return chk.Err("inconsistent results: saturation must be greater than minimum saturation. sl = %g < %g is incorrect", sl, slmin)
	}

	// set state
	s.A_sl = sl          // 2
	s.A_ρL += o.Cl * Δpl // 3
	s.A_ρG += o.Cg * Δpg // 4
	s.A_Δpc = Δpc        // 5
	s.A_wet = wet        // 6
	return
}

// Ccb (Cc-bar) returns dsl/dpc consistent with the update method
//  See Eq. (54) on page 618 of [1]
func (o Model) Ccb(s *State, pc float64) (dsldpc float64, err error) {
	sl := s.A_sl
	wet := s.A_wet
	Δpc := s.A_Δpc
	f, err := o.Lrm.Cc(pc, sl, wet) // @ n+1
	if err != nil {
		return
	}
	if o.Ncns { // non consistent
		dsldpc = f
		return
	}
	L, err := o.Lrm.L(pc, sl, wet) // @ n+1
	if err != nil {
		return
	}
	J, err := o.Lrm.J(pc, sl, wet) // @ n+1
	if err != nil {
		return
	}
	dsldpc = (f + Δpc*L) / (1.0 - Δpc*J)
	return
}

// Ccd (Cc-dash) returns dCc/dpc consistent with the update method
//  See Eqs. (55) and (56) on page 618 of [1]
func (o Model) Ccd(s *State, pc float64) (dCcdpc float64, err error) {
	sl := s.A_sl
	wet := s.A_wet
	Δpc := s.A_Δpc
	if o.Ncns || o.Ncns2 { // non consistent
		dCcdpc, err = o.Lrm.L(pc, sl, wet) // @ n+1
		return
	}
	f, err := o.Lrm.Cc(pc, sl, wet) // @ n+1
	if err != nil {
		return
	}
	L, Lx, J, Jx, Jy, err := o.Lrm.Derivs(pc, sl, wet)
	if err != nil {
		return
	}
	Ly := Jx
	Ccb := (f + Δpc*L) / (1.0 - Δpc*J)
	LL := Lx + Ly*Ccb
	JJ := Jx + Jy*Ccb
	dCcdpc = (2.0*L + Δpc*LL + (2.0*J+Δpc*JJ)*Ccb) / (1.0 - Δpc*J)
	return
}

// GetModel returns (existent or new) model for porous media
//  simfnk    -- unique simulation filename key
//  matname   -- name of material
//  getnew    -- force a new allocation; i.e. do not use any model found in database
//  Note: returns nil on errors
func GetModel(simfnk, matname string, getnew bool) *Model {

	// get new model, regardless whether it exists in database or not
	if getnew {
		return new(Model)
	}

	// search database
	key := io.Sf("%s_%s", simfnk, matname)
	if model, ok := _models[key]; ok {
		return model
	}

	// if not found, get new
	model := new(Model)
	_models[key] = model
	return model
}

// LogModels prints to log information on existent and allocated Models
func LogModels() {
	l := "mporous: allocated:"
	for key, _ := range _models {
		l += " " + io.Sf("%q", key)
	}
	log.Println(l)
}

// _models holds pre-allocated models
var _models = map[string]*Model{}
