// FinCore module manifest for multi-package import test
module demoapp

finc 0.7

require (
	extlib ./vendor/extlib
)

replace oldgreet => ./vendor/extlib/greet
