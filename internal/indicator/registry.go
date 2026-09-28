package indicator

func init() {
	Register(maCalc{})
	Register(emaCalc{})
	Register(macdCalc{})
	Register(rsiCalc{})
	Register(kdjCalc{})
	Register(bollCalc{})
	Register(atrCalc{})
}
