// Copyright 2015 Dorival Pedroso and Raul Durand. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package fem

import (
	"github.com/cpmech/gofem/inp"
	"github.com/cpmech/gofem/shp"

	"github.com/cpmech/gosl/chk"
	"github.com/cpmech/gosl/fun"
	"github.com/cpmech/gosl/la"
	"github.com/cpmech/gosl/tsr"
)

// ElemUP represents an element for porous media based on the u-p formulation [1]
//  References:
//   [1] Pedroso DM (2015) A consistent u-p formulation for porous media with hysteresis.
//       Int Journal for Numerical Methods in Engineering, 101(8) 606-634
//       http://dx.doi.org/10.1002/nme.4808
//   [2] Pedroso DM (2015) A solution to transient seepage in unsaturated porous media.
//       Computer Methods in Applied Mechanics and Engineering, 285 791-816,
//       http://dx.doi.org/10.1016/j.cma.2014.12.009
type ElemUP struct {

	// auxiliary
	Fconds []*FaceCond // face conditions; e.g. seepage faces
	CtypeU string      // u: cell type
	CtypeP string      // p: cell type

	// underlying elements
	U *ElemU // u-element
	P *ElemP // p-element

	// scratchpad. computed @ each ip
	divus float64     // divus
	bs    []float64   // bs = as - g = α1・u - ζs - g; (Eqs 35b and A.1 [1]) with 'as' being the acceleration of solids and g, gravity
	hl    []float64   // hl = -ρL・bs - ∇pl; Eq (A.1) of [1]
	Kup   [][]float64 // [nu][np] Kup := dRus/dpl consistent tangent matrix
	Kpu   [][]float64 // [np][nu] Kpu := dRpl/dus consistent tangent matrix

	// for seepage face derivatives
	dρldus_ex [][]float64 // [nverts][nverts*ndim] ∂ρl/∂us extrapolted to nodes => if has qb (flux)
}

// initialisation ///////////////////////////////////////////////////////////////////////////////////

// register element
func init() {

	// information allocator
	infogetters["up"] = func(cellType string, faceConds []*FaceCond) *Info {

		// new info
		var info Info

		// p-element cell type
		p_cellType := cellType
		lbb := !Global.Sim.Data.NoLBB
		if lbb {
			p_cellType = shp.GetBasicType(cellType)
		}

		// underlying cells info
		u_info := infogetters["u"](cellType, faceConds)
		p_info := infogetters["p"](p_cellType, faceConds)

		// solution variables
		nverts := shp.GetNverts(cellType)
		info.Dofs = make([][]string, nverts)
		for i, dofs := range u_info.Dofs {
			info.Dofs[i] = append(info.Dofs[i], dofs...)
		}
		for i, dofs := range p_info.Dofs {
			info.Dofs[i] = append(info.Dofs[i], dofs...)
		}

		// maps
		info.Y2F = u_info.Y2F
		for key, val := range p_info.Y2F {
			info.Y2F[key] = val
		}

		// t1 and t2 variables
		info.T1vars = p_info.T1vars
		info.T2vars = u_info.T2vars
		return &info
	}

	// element allocator
	eallocators["up"] = func(cellType string, faceConds []*FaceCond, cid int, edat *inp.ElemData, x [][]float64) Elem {

		// basic data
		var o ElemUP
		o.Fconds = faceConds

		// p-element cell type
		p_cellType := cellType
		lbb := !Global.Sim.Data.NoLBB
		if lbb {
			p_cellType = shp.GetBasicType(cellType)
		}

		// cell types
		o.CtypeU = cellType
		o.CtypeP = p_cellType

		// allocate u element
		u_allocator := eallocators["u"]
		u_elem := u_allocator(cellType, faceConds, cid, edat, x)
		if LogErrCond(u_elem == nil, "cannot allocate underlying u-element") {
			return nil
		}
		o.U = u_elem.(*ElemU)

		// make sure p-element uses the same nubmer of integration points than u-element
		edat.Nip = len(o.U.IpsElem)
		//edat.Nipf = len(o.U.IpsFace) // TODO: check if this is necessary

		// allocate p-element
		p_allocator := eallocators["p"]
		p_elem := p_allocator(p_cellType, faceConds, cid, edat, x)
		if LogErrCond(p_elem == nil, "cannot allocate underlying p-element") {
			return nil
		}
		o.P = p_elem.(*ElemP)

		// scratchpad. computed @ each ip
		ndim := Global.Ndim
		o.bs = make([]float64, ndim)
		o.hl = make([]float64, ndim)
		o.Kup = la.MatAlloc(o.U.Nu, o.P.Np)
		o.Kpu = la.MatAlloc(o.P.Np, o.U.Nu)

		// seepage terms
		if o.P.DoExtrap {
			p_nverts := o.P.Shp.Nverts
			u_nverts := o.U.Shp.Nverts
			o.dρldus_ex = la.MatAlloc(p_nverts, u_nverts*ndim)
		}

		// return new element
		return &o
	}
}

// implementation ///////////////////////////////////////////////////////////////////////////////////

// Id returns the cell Id
func (o ElemUP) Id() int { return o.U.Id() }

// SetEqs set equations
func (o *ElemUP) SetEqs(eqs [][]int, mixedform_eqs []int) (ok bool) {

	// u: equations
	u_getter := infogetters["u"]
	u_info := u_getter(o.CtypeU, o.Fconds)
	u_nverts := len(u_info.Dofs)
	u_eqs := make([][]int, u_nverts)
	for i := 0; i < u_nverts; i++ {
		nkeys := len(u_info.Dofs[i])
		u_eqs[i] = make([]int, nkeys)
		for j := 0; j < nkeys; j++ {
			u_eqs[i][j] = eqs[i][j]
		}
	}

	// p: equations
	p_getter := infogetters["p"]
	p_info := p_getter(o.CtypeP, o.Fconds)
	p_nverts := len(p_info.Dofs)
	p_eqs := make([][]int, p_nverts)
	for i := 0; i < p_nverts; i++ {
		start := len(u_info.Dofs[i])
		nkeys := len(p_info.Dofs[i])
		p_eqs[i] = make([]int, nkeys)
		for j := 0; j < nkeys; j++ {
			p_eqs[i][j] = eqs[i][start+j]
		}
	}

	// set equations
	if !o.U.SetEqs(u_eqs, mixedform_eqs) {
		return
	}
	return o.P.SetEqs(p_eqs, nil)
}

// SetEleConds set element conditions
func (o *ElemUP) SetEleConds(key string, f fun.Func, extra string) (ok bool) {
	if !o.U.SetEleConds(key, f, extra) {
		return
	}
	return o.P.SetEleConds(key, f, extra)
}

// InterpStarVars interpolates star variables to integration points
func (o *ElemUP) InterpStarVars(sol *Solution) (ok bool) {

	// for each integration point
	ndim := Global.Ndim
	u_nverts := o.U.Shp.Nverts
	p_nverts := o.P.Shp.Nverts
	var r int
	for idx, ip := range o.U.IpsElem {

		// interpolation functions and gradients
		if LogErr(o.P.Shp.CalcAtIp(o.P.X, ip, true), "InterpStarVars") {
			return
		}
		if LogErr(o.U.Shp.CalcAtIp(o.U.X, ip, true), "InterpStarVars") {
			return
		}
		S := o.U.Shp.S
		G := o.U.Shp.G
		Sb := o.P.Shp.S

		// clear local variables
		o.P.ψl[idx], o.U.divχs[idx] = 0, 0
		for i := 0; i < ndim; i++ {
			o.U.ζs[idx][i], o.U.χs[idx][i] = 0, 0
		}

		// p-variables
		for m := 0; m < p_nverts; m++ {
			r = o.P.Pmap[m]
			o.P.ψl[idx] += Sb[m] * sol.Psi[r]
		}

		// u-variables
		for m := 0; m < u_nverts; m++ {
			for i := 0; i < ndim; i++ {
				r = o.U.Umap[i+m*ndim]
				o.U.ζs[idx][i] += S[m] * sol.Zet[r]
				o.U.χs[idx][i] += S[m] * sol.Chi[r]
				o.U.divχs[idx] += G[m][i] * sol.Chi[r]
			}
		}
	}
	return true
}

// adds -R to global residual vector fb
func (o ElemUP) AddToRhs(fb []float64, sol *Solution) (ok bool) {

	// clear variables
	if o.P.DoExtrap {
		la.VecFill(o.P.ρl_ex, 0)
	}
	if o.U.UseB {
		la.VecFill(o.U.fi, 0)
	}

	// for each integration point
	dc := Global.DynCoefs
	ndim := Global.Ndim
	u_nverts := o.U.Shp.Nverts
	p_nverts := o.P.Shp.Nverts
	var coef, plt, klr, ρl, ρ, p, Cpl, Cvs, divvs float64
	var r int
	for idx, ip := range o.U.IpsElem {

		// interpolation functions, gradients and variables @ ip
		if !o.ipvars(idx, sol) {
			return
		}
		coef = o.U.Shp.J * ip.W
		S := o.U.Shp.S
		G := o.U.Shp.G
		Sb := o.P.Shp.S
		Gb := o.P.Shp.G

		// axisymmetric case
		radius := 1.0
		if Global.Sim.Data.Axisym {
			radius = o.U.Shp.AxisymGetRadius(o.U.X)
			coef *= radius
		}

		// auxiliary
		σe := o.U.States[idx].Sig
		divvs = dc.α4*o.divus - o.U.divχs[idx] // divergence of Eq. (35a) [1]

		// tpm variables
		plt = dc.β1*o.P.pl - o.P.ψl[idx] // Eq. (35c) [1]
		klr = o.P.Mdl.Cnd.Klr(o.P.States[idx].A_sl)
		if LogErr(o.P.Mdl.CalcLs(o.P.res, o.P.States[idx], o.P.pl, o.divus, false), "AddToRhs") {
			return
		}
		ρl = o.P.res.A_ρl
		ρ = o.P.res.A_ρ
		p = o.P.res.A_p
		Cpl = o.P.res.Cpl
		Cvs = o.P.res.Cvs

		// compute ρwl. see Eq (34b) and (35) of [1]
		for i := 0; i < ndim; i++ {
			o.P.ρwl[i] = 0
			for j := 0; j < ndim; j++ {
				o.P.ρwl[i] += klr * o.P.Mdl.Klsat[i][j] * o.hl[j]
			}
		}

		// p: add negative of residual term to fb; see Eqs. (38a) and (45a) of [1]
		for m := 0; m < p_nverts; m++ {
			r = o.P.Pmap[m]
			fb[r] -= coef * Sb[m] * (Cpl*plt + Cvs*divvs)
			for i := 0; i < ndim; i++ {
				fb[r] += coef * Gb[m][i] * o.P.ρwl[i] // += coef * div(ρl*wl)
			}
			if o.P.DoExtrap { // Eq. (19) of [2]
				o.P.ρl_ex[m] += o.P.Emat[m][idx] * ρl
			}
		}

		// u: add negative of residual term to fb; see Eqs. (38b) and (45b) [1]
		if o.U.UseB {
			IpBmatrix(o.U.B, ndim, u_nverts, G, radius, S)
			la.MatTrVecMulAdd(o.U.fi, coef, o.U.B, σe) // fi += coef * tr(B) * σ
			for m := 0; m < u_nverts; m++ {
				for i := 0; i < ndim; i++ {
					r = o.U.Umap[i+m*ndim]
					fb[r] -= coef * S[m] * ρ * o.bs[i]
					fb[r] += coef * p * G[m][i]
				}
			}
		} else {
			for m := 0; m < u_nverts; m++ {
				for i := 0; i < ndim; i++ {
					r = o.U.Umap[i+m*ndim]
					fb[r] -= coef * S[m] * ρ * o.bs[i]
					for j := 0; j < ndim; j++ {
						fb[r] -= coef * tsr.M2T(σe, i, j) * G[m][j]
					}
					fb[r] += coef * p * G[m][i]
				}
			}
		}
	}

	// add fi term to fb, if using B matrix
	if o.U.UseB {
		for i, I := range o.U.Umap {
			fb[I] -= o.U.fi[i]
		}
	}

	// external forces
	if len(o.U.NatBcs) > 0 {
		if !o.U.add_surfloads_to_rhs(fb, sol) {
			return
		}
	}

	// contribution from natural boundary conditions
	if len(o.P.NatBcs) > 0 {
		return o.P.add_natbcs_to_rhs(fb, sol)
	}
	return true
}

// adds element K to global Jacobian matrix Kb
func (o ElemUP) AddToKb(Kb *la.Triplet, sol *Solution, firstIt bool) (ok bool) {

	// clear matrices
	ndim := Global.Ndim
	u_nverts := o.U.Shp.Nverts
	p_nverts := o.P.Shp.Nverts
	la.MatFill(o.P.Kpp, 0)
	for i := 0; i < o.U.Nu; i++ {
		for j := 0; j < o.P.Np; j++ {
			o.Kup[i][j] = 0
			o.Kpu[j][i] = 0
		}
		for j := 0; j < o.U.Nu; j++ {
			o.U.K[i][j] = 0
		}
	}
	if o.P.DoExtrap {
		for i := 0; i < p_nverts; i++ {
			o.P.ρl_ex[i] = 0
			for j := 0; j < p_nverts; j++ {
				o.P.dρldpl_ex[i][j] = 0
			}
			for j := 0; j < o.U.Nu; j++ {
				o.dρldus_ex[i][j] = 0
			}
		}
	}

	// for each integration point
	dc := Global.DynCoefs
	var coef, plt, klr, ρL, Cl, divvs float64
	var ρl, ρ, Cpl, Cvs, dρdpl, dpdpl, dCpldpl, dCvsdpl, dklrdpl, dCpldusM, dρldusM, dρdusM float64
	var r, c int
	for idx, ip := range o.U.IpsElem {

		// interpolation functions, gradients and variables @ ip
		if !o.ipvars(idx, sol) {
			return
		}
		coef = o.U.Shp.J * ip.W
		S := o.U.Shp.S
		G := o.U.Shp.G
		Sb := o.P.Shp.S
		Gb := o.P.Shp.G

		// axisymmetric case
		radius := 1.0
		if Global.Sim.Data.Axisym {
			radius = o.U.Shp.AxisymGetRadius(o.U.X)
			coef *= radius
		}

		// auxiliary
		divvs = dc.α4*o.divus - o.U.divχs[idx] // divergence of Eq (35a) [1]

		// tpm variables
		plt = dc.β1*o.P.pl - o.P.ψl[idx] // Eq (35c) [1]
		klr = o.P.Mdl.Cnd.Klr(o.P.States[idx].A_sl)
		ρL = o.P.States[idx].A_ρL
		Cl = o.P.Mdl.Cl
		if LogErr(o.P.Mdl.CalcLs(o.P.res, o.P.States[idx], o.P.pl, o.divus, true), "AddToKb") {
			return
		}
		ρl = o.P.res.A_ρl
		ρ = o.P.res.A_ρ
		Cpl = o.P.res.Cpl
		Cvs = o.P.res.Cvs
		dρdpl = o.P.res.Dρdpl
		dpdpl = o.P.res.Dpdpl
		dCpldpl = o.P.res.DCpldpl
		dCvsdpl = o.P.res.DCvsdpl
		dklrdpl = o.P.res.Dklrdpl
		dCpldusM = o.P.res.DCpldusM
		dρldusM = o.P.res.DρldusM
		dρdusM = o.P.res.DρdusM

		// Kpu, Kup and Kpp
		for n := 0; n < p_nverts; n++ {
			for j := 0; j < ndim; j++ {

				// Kpu := ∂Rl^n/∂us^m and Kup := ∂Rus^m/∂pl^n; see Eq (47) of [1]
				for m := 0; m < u_nverts; m++ {
					c = j + m*ndim

					// add ∂rlb/∂us^m: Eqs (A.3) and (A.6) of [1]
					o.Kpu[n][c] += coef * Sb[n] * (dCpldusM*plt + dc.α4*Cvs) * G[m][j]

					// add ∂(ρl.wl)/∂us^m: Eq (A.8) of [1]
					for i := 0; i < ndim; i++ {
						o.Kpu[n][c] += coef * Gb[n][i] * S[m] * dc.α1 * ρL * klr * o.P.Mdl.Klsat[i][j]
					}

					// add ∂rl/∂pl^n and ∂p/∂pl^n: Eqs (A.9) and (A.11) of [1]
					o.Kup[c][n] += coef * (S[m]*Sb[n]*dρdpl*o.bs[j] - G[m][j]*Sb[n]*dpdpl)

					// for seepage face
					if o.P.DoExtrap {
						o.dρldus_ex[n][c] += o.P.Emat[n][idx] * dρldusM * G[m][j]
					}
				}

				// term in brackets in Eq (A.7) of [1]
				o.P.tmp[j] = Sb[n]*dklrdpl*o.hl[j] - klr*(Sb[n]*Cl*o.bs[j]+Gb[n][j])
			}

			// Kpp := ∂Rl^m/∂pl^n; see Eq (47) of [1]
			for m := 0; m < p_nverts; m++ {

				// add ∂rlb/dpl^n: Eq (A.5) of [1]
				o.P.Kpp[m][n] += coef * Sb[m] * Sb[n] * (dCpldpl*plt + dCvsdpl*divvs + dc.β1*Cpl)

				// add ∂(ρl.wl)/∂us^m: Eq (A.7) of [1]
				for i := 0; i < ndim; i++ {
					for j := 0; j < ndim; j++ {
						o.P.Kpp[m][n] -= coef * Gb[m][i] * o.P.Mdl.Klsat[i][j] * o.P.tmp[j]
					}
				}

				// inner summation term in Eq (22) of [2]
				if o.P.DoExtrap {
					o.P.dρldpl_ex[m][n] += o.P.Emat[m][idx] * Cpl * Sb[n]
				}
			}

			// Eq. (19) of [2]
			if o.P.DoExtrap {
				o.P.ρl_ex[n] += o.P.Emat[n][idx] * ρl
			}
		}

		// Kuu: add ∂rub^m/∂us^n; see Eqs (47) and (A.10) of [1]
		for m := 0; m < u_nverts; m++ {
			for i := 0; i < ndim; i++ {
				r = i + m*ndim
				for n := 0; n < u_nverts; n++ {
					for j := 0; j < ndim; j++ {
						c = j + n*ndim
						o.U.K[r][c] += coef * S[m] * (S[n]*dc.α1*ρ*tsr.It[i][j] + dρdusM*o.bs[i]*G[n][j])
					}
				}
			}
		}

		// consistent tangent model matrix
		if LogErr(o.U.MdlSmall.CalcD(o.U.D, o.U.States[idx], firstIt), "AddToKb") {
			return
		}

		// Kuu: add stiffness term ∂(σe・G^m)/∂us^n
		if o.U.UseB {
			IpBmatrix(o.U.B, ndim, u_nverts, G, radius, S)
			la.MatTrMulAdd3(o.U.K, coef, o.U.B, o.U.D, o.U.B) // K += coef * tr(B) * D * B
		} else {
			IpAddToKt(o.U.K, u_nverts, ndim, coef, G, o.U.D)
		}
	}

	// contribution from natural boundary conditions
	if o.P.HasSeep {
		if !o.P.add_natbcs_to_jac(sol) {
			return
		}
		if !o.add_natbcs_to_jac(sol) {
			return
		}
	}

	// add K to sparse matrix Kb
	//    _             _
	//   |  Kuu Kup  0   |
	//   |  Kpu Kpp Kpf  |
	//   |_ Kfu Kfp Kff _|
	//
	for i, I := range o.P.Pmap {
		for j, J := range o.P.Pmap {
			Kb.Put(I, J, o.P.Kpp[i][j])
		}
		for j, J := range o.P.Fmap {
			Kb.Put(I, J, o.P.Kpf[i][j])
			Kb.Put(J, I, o.P.Kfp[j][i])
		}
		for j, J := range o.U.Umap {
			Kb.Put(I, J, o.Kpu[i][j])
			Kb.Put(J, I, o.Kup[j][i])
		}
	}
	for i, I := range o.P.Fmap {
		for j, J := range o.P.Fmap {
			Kb.Put(I, J, o.P.Kff[i][j])
		}
	}
	for i, I := range o.U.Umap {
		for j, J := range o.U.Umap {
			Kb.Put(I, J, o.U.K[i][j])
		}
	}
	return true
}

// Update perform (tangent) update
func (o *ElemUP) Update(sol *Solution) (ok bool) {
	if !o.U.Update(sol) {
		return
	}
	return o.P.Update(sol)
}

// internal variables ///////////////////////////////////////////////////////////////////////////////

// Ipoints returns the real coordinates of integration points [nip][ndim]
func (o ElemUP) Ipoints() (coords [][]float64) {
	coords = la.MatAlloc(len(o.U.IpsElem), Global.Ndim)
	for idx, ip := range o.U.IpsElem {
		coords[idx] = o.U.Shp.IpRealCoords(o.U.X, ip)
	}
	return
}

// SetIniIvs sets initial ivs for given values in sol and ivs map
func (o *ElemUP) SetIniIvs(sol *Solution, ivs map[string][]float64) (ok bool) {

	// set p-element first
	if !o.P.SetIniIvs(sol, nil) {
		return
	}

	// initial stresses given
	if _, okk := ivs["svT"]; okk {

		// total vertical stresses and K0
		nip := len(o.U.IpsElem)
		svT := ivs["svT"]
		K0s := ivs["K0"]
		chk.IntAssert(len(svT), nip)
		chk.IntAssert(len(K0s), 1)
		K0 := K0s[0]

		// for each integration point
		sx := make([]float64, nip)
		sy := make([]float64, nip)
		sz := make([]float64, nip)
		for i, ip := range o.U.IpsElem {

			// compute pl @ ip
			if LogErr(o.P.Shp.CalcAtIp(o.P.X, ip, false), "SetIniIvs") {
				return
			}
			pl := 0.0
			for m := 0; m < o.P.Shp.Nverts; m++ {
				pl += o.P.Shp.S[m] * sol.Y[o.P.Pmap[m]]
			}

			// compute effective stresses
			p := pl * o.P.States[i].A_sl
			svE := svT[i] + p
			shE := K0 * svE
			sx[i], sy[i], sz[i] = shE, svE, shE
			if Global.Ndim == 3 {
				sx[i], sy[i], sz[i] = shE, shE, svE
			}
		}
		ivs = map[string][]float64{"sx": sx, "sy": sy, "sz": sz}
	}

	// set u-element
	return o.U.SetIniIvs(sol, ivs)
}

// BackupIvs create copy of internal variables
func (o *ElemUP) BackupIvs(aux bool) (ok bool) {
	if !o.U.BackupIvs(aux) {
		return
	}
	return o.P.BackupIvs(aux)
}

// RestoreIvs restore internal variables from copies
func (o *ElemUP) RestoreIvs(aux bool) (ok bool) {
	if !o.U.RestoreIvs(aux) {
		return
	}
	return o.P.RestoreIvs(aux)
}

// Ureset fixes internal variables after u (displacements) have been zeroed
func (o *ElemUP) Ureset(sol *Solution) (ok bool) {
	ndim := Global.Ndim
	u_nverts := o.U.Shp.Nverts
	for idx, ip := range o.U.IpsElem {
		if LogErr(o.U.Shp.CalcAtIp(o.U.X, ip, true), "Update") {
			return
		}
		G := o.U.Shp.G
		var divus float64
		for m := 0; m < u_nverts; m++ {
			for i := 0; i < ndim; i++ {
				r := o.U.Umap[i+m*ndim]
				divus += G[m][i] * sol.Y[r]
			}
		}
		o.P.States[idx].A_ns0 = (1.0 - divus) * (1.0 - o.P.Mdl.Nf0)
		o.P.StatesBkp[idx].A_ns0 = o.P.States[idx].A_ns0
	}
	if !o.U.Ureset(sol) {
		return
	}
	return o.P.Ureset(sol)
}

// writer ///////////////////////////////////////////////////////////////////////////////////////////

// Encode encodes internal variables
func (o ElemUP) Encode(enc Encoder) (ok bool) {
	if !o.U.Encode(enc) {
		return
	}
	return o.P.Encode(enc)
}

// Decode decodes internal variables
func (o ElemUP) Decode(dec Decoder) (ok bool) {
	if !o.U.Decode(dec) {
		return
	}
	return o.P.Decode(dec)
}

// OutIpsData returns data from all integration points for output
func (o ElemUP) OutIpsData() (data []*OutIpData) {
	ndim := Global.Ndim
	flow := FlowKeys()
	sigs := StressKeys()
	for idx, ip := range o.U.IpsElem {
		r := o.P.States[idx]
		s := o.U.States[idx]
		x := o.U.Shp.IpRealCoords(o.U.X, ip)
		calc := func(sol *Solution) (vals map[string]float64) {
			if !o.ipvars(idx, sol) {
				return
			}
			ns := (1.0 - o.divus) * o.P.States[idx].A_ns0
			ρL := r.A_ρL
			klr := o.P.Mdl.Cnd.Klr(r.A_sl)
			vals = map[string]float64{
				"sl": r.A_sl,
				"pl": o.P.pl,
				"nf": 1.0 - ns,
			}
			for i := 0; i < ndim; i++ {
				for j := 0; j < ndim; j++ {
					vals[flow[i]] += klr * o.P.Mdl.Klsat[i][j] * o.hl[j] / ρL
				}
			}
			for i, _ := range sigs {
				vals[sigs[i]] = s.Sig[i]
			}
			return
		}
		data = append(data, &OutIpData{o.Id(), x, calc})
	}
	return
}

// auxiliary ////////////////////////////////////////////////////////////////////////////////////////

// ipvars computes current values @ integration points. idx == index of integration point
func (o *ElemUP) ipvars(idx int, sol *Solution) (ok bool) {

	// interpolation functions and gradients
	if LogErr(o.P.Shp.CalcAtIp(o.P.X, o.U.IpsElem[idx], true), "ipvars") {
		return
	}
	if LogErr(o.U.Shp.CalcAtIp(o.U.X, o.U.IpsElem[idx], true), "ipvars") {
		return
	}

	// auxiliary
	ndim := Global.Ndim
	dc := Global.DynCoefs
	ρL := o.P.States[idx].A_ρL
	o.P.compute_gvec(sol.T)

	// clear gpl and recover u-variables @ ip
	o.divus = 0
	for i := 0; i < ndim; i++ {
		o.P.gpl[i] = 0 // clear gpl here
		o.U.us[i] = 0
		for m := 0; m < o.U.Shp.Nverts; m++ {
			r := o.U.Umap[i+m*ndim]
			o.U.us[i] += o.U.Shp.S[m] * sol.Y[r]
			o.divus += o.U.Shp.G[m][i] * sol.Y[r]
		}
	}

	// recover p-variables @ ip
	o.P.pl = 0
	for m := 0; m < o.P.Shp.Nverts; m++ {
		r := o.P.Pmap[m]
		o.P.pl += o.P.Shp.S[m] * sol.Y[r]
		for i := 0; i < ndim; i++ {
			o.P.gpl[i] += o.P.Shp.G[m][i] * sol.Y[r]
		}
	}

	// compute bs and hl. see Eqs (A.1) of [1]
	for i := 0; i < ndim; i++ {
		o.bs[i] = dc.α1*o.U.us[i] - o.U.ζs[idx][i] - o.P.g[i]
		o.hl[i] = -ρL*o.bs[i] - o.P.gpl[i]
	}
	return true
}

// add_natbcs_to_jac adds contribution from natural boundary conditions to Jacobian
func (o ElemUP) add_natbcs_to_jac(sol *Solution) (ok bool) {

	// compute surface integral
	ndim := Global.Ndim
	u_nverts := o.U.Shp.Nverts
	var shift float64
	var pl, fl, plmax, g, rmp float64
	for idx, nbc := range o.P.NatBcs {

		// plmax shift
		shift = nbc.Fcn.F(sol.T, nil)

		// loop over ips of face
		for jdx, ipf := range o.P.IpsFace {

			// interpolation functions and gradients @ face
			iface := nbc.IdxFace
			if LogErr(o.P.Shp.CalcAtFaceIp(o.P.X, ipf, iface), "up: add_natbcs_to_jac") {
				return
			}
			Sf := o.P.Shp.Sf
			Jf := la.VecNorm(o.P.Shp.Fnvec)
			coef := ipf.W * Jf

			// select natural boundary condition type
			switch nbc.Key {
			case "seep":

				// variables extrapolated to face
				_, pl, fl = o.P.fipvars(iface, sol)
				plmax = o.P.Plmax[idx][jdx] - shift
				if plmax < 0 {
					plmax = 0
				}

				// compute derivatives
				g = pl - plmax // Eq. (24)
				rmp = o.P.ramp(fl + o.P.κ*g)
				for i, m := range o.P.Shp.FaceLocalV[iface] {
					for n := 0; n < u_nverts; n++ {
						for j := 0; j < ndim; j++ {
							c := j + n*ndim
							for l, r := range o.P.Shp.FaceLocalV[iface] {
								o.Kpu[m][c] += coef * Sf[i] * Sf[l] * o.dρldus_ex[r][c] * rmp
							}
						}
					}
				}
			}
		}
	}
	return true
}

func (o ElemUP) debug_print_K() {
	la.PrintMat("Kpp", o.P.Kpp, "%20.10f", false)
	la.PrintMat("Kpf", o.P.Kpf, "%20.10f", false)
	la.PrintMat("Kfp", o.P.Kfp, "%20.10f", false)
	la.PrintMat("Kff", o.P.Kff, "%20.10f", false)
	la.PrintMat("Kpu", o.Kpu, "%20.10f", false)
	la.PrintMat("Kup", o.Kup, "%20.10f", false)
	la.PrintMat("Kuu", o.U.K, "%20.10f", false)
}
