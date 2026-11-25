%{
  "http/write-1" => [
    {"fx:Direct.a/0", "https://api.stripe.com", "/v1/charges"},
    {"fx:Direct.b/0", "https://api.stripe.com", "/v1/refunds"},
    {"fx:Interp.get/1", "Interp", "/check"},
    {"fx:Interp2.get/1", "Interp2", "/v1/x"},
    {"fx:Loop.go/0", "Loop", "/api/check"},
    {"fx:Mixed.charge/0", "https://api.stripe.com", "/v1/charges"},
    {"fx:Mixed.check/0", "http://admin.breakdown.svc:4000", "/api/check"},
    {"fx:OtherFn.go/0", "OtherFn", "/x"},
    {"fx:Upper.go/0", "https://api.stripe.com", "/v1/x"},
    {"fx:V6.go/0", "http://[2001:db8::1]:8080", "/v1/x"},
    {"fx:VarNoise.go/0", "VarNoise", "/api/check"}
  ],
  "http2/write-1" => [
    {"fx:CfgFallback.charge/0", "https://api.stripe.com", "/v1/charges"},
    {"fx:CfgFallback.check/0", "CfgFallback", "/api/check"},
    {"fx:HelperMixed.charge/0", "HelperMixed", "/charges"},
    {"fx:HelperMixed.check/0", "HelperMixed", "/api/check"},
    {"fx:HelperShape.charge/0", "https://api.stripe.com", "/v1/charges"},
    {"fx:InterpFallback.charge/0", "https://api.stripe.com", "/v1/charges"},
    {"fx:InterpFallback.check/1", "InterpFallback", "/api/check"},
    {"fx:ParamFallback.charge/0", "ParamFallback", "/v1/charges"},
    {"fx:ParamFallback.check/0", "ParamFallback", "/api/check"}
  ],
  "http3/write-1" => [
    {"fx:CaseMixed.charge/0", "CaseMixed", "/charges"},
    {"fx:CaseMixed.check/0", "CaseMixed", "/api/check"},
    {"fx:ConcatUrl.charge/0", "https://api.stripe.com", "/v1/charges"},
    {"fx:ConcatUrl.check3/1", "https://api.stripe.com", "/v1/api/check3"},
    {"fx:GetEnvDefault.charge/0", "GetEnvDefault", "/charges"},
    {"fx:GetEnvDefault.check/0", "GetEnvDefault", "/api/check"},
    {"fx:Loopback.charge/0", "Loopback", "/charges"},
    {"fx:Loopback.local/0", "Loopback", "/x"}
  ],
  "http4/write-1" => [
    {"fx:Upper.b/0", "https://api.stripe.com", "/v1/charges"},
    {"fx:Upper.c/0", "https://api.stripe.com", "/x"}
  ],
  "http5/write-1" => [
    {"fx:Concat.charge/0", "Concat", "/charges"},
    {"fx:EnvCase.charge/0", "EnvCase", "/charges"},
    {"fx:FinanceShape.list/0", "FinanceShape", "/charges"},
    {"fx:OrDefault.a/1", "OrDefault", "/v1/messages"},
    {"fx:OrDefault.b/1", "https://api.anthropic.com", "/v1/messages"},
    {"fx:ParamBranch.charge/0", "ParamBranch", "/charges"},
    {"fx:ParamBranch.check/1", "ParamBranch", "/api/check"},
    {"fx:Sigil.charge/0", "Sigil", "/charges"},
    {"fx:TupleKey.charge/0", "TupleKey", "/charges"},
    {"fx:TupleLit.charge/0", "TupleLit", "/charges"},
    {"fx:VarBranch.check/0", "VarBranch", "/api/check"}
  ],
  "http6/write-1" => [
    {"fx:VarCase.charge/0", "VarCase", "/charges"},
    {"fx:VarCase.check/0", "VarCase", "/api/check"}
  ],
  "http7/write-1" => [
    {"fx:S1FnParam.ping/1", "S1FnParam", "/health"},
    {"fx:S2CasePattern.ping/1", "S2CasePattern", "/health"},
    {"fx:S3With.ping/1", "S3With", "/health"},
    {"fx:S4For.ping/0", "S4For", "/health"},
    {"fx:S5InnerFnBinding.ping/1", "S5InnerFnBinding", "/health"},
    {"fx:T10Rebind.ping/1", "T10Rebind", "/x"},
    {"fx:T11List.ping/0", "T11List", "/x"},
    {"fx:T12Pipe.ping/1", "T12Pipe", "/x"},
    {"fx:T13EmptyBlock.ping/0", "T13EmptyBlock", "/x"},
    {"fx:T1TwoHosts.mercury/0", "T1TwoHosts", "/accounts"},
    {"fx:T1TwoHosts.stripe/0", "T1TwoHosts", "/charges"},
    {"fx:T2IfNoElse.ping/1", "T2IfNoElse", "/x"},
    {"fx:T3Unless.ping/1", "T3Unless", "/x"},
    {"fx:T4CondVar.ping/1", "T4CondVar", "/x"},
    {"fx:T5Block.ping/1", "T5Block", "/x"},
    {"fx:T6IfBlock.ping/1", "T6IfBlock", "/x"},
    {"fx:T7Attr.lit/0", "T7Attr", "/y"},
    {"fx:T7Attr.ping/1", "T7Attr", "/x"},
    {"fx:T8Nested.ping/0", "T8Nested", "/x"},
    {"fx:T9ThreeTuple.ping/0", "T9ThreeTuple", "/x"},
    {"fx:T9ThreeTuple.wrong_len/0", "T9ThreeTuple", "/x"}
  ],
  "http8/write-1" => [
    {"fx:V10OtherWith.ping/1", "V10OtherWith", "/x"},
    {"fx:V11Default.ping/1", "V11Default", "/x"},
    {"fx:V12Guard.ping/1", "V12Guard", "/x"},
    {"fx:V13Bodyless.ping/1", "V13Bodyless", "/x"},
    {"fx:V14Delegate.ping/0", "V14Delegate", "/x"},
    {"fx:V1ImplicitTry.ping/0", "V1ImplicitTry", "/x"},
    {"fx:V2ExplicitTry.ping/0", "V2ExplicitTry", "/x"},
    {"fx:V3NestedUse.ping/1", "V3NestedUse", "/x"},
    {"fx:V4ParamThenRebind.ping/1", "V4ParamThenRebind", "/a"},
    {"fx:V4ParamThenRebind.ping/1", "V4ParamThenRebind", "/b"},
    {"fx:V5SeqTwoHosts.ping/0", "V5SeqTwoHosts", "/a"},
    {"fx:V5SeqTwoHosts.ping/0", "V5SeqTwoHosts", "/b"},
    {"fx:V6CondUse.ping/1", "V6CondUse", "/x"},
    {"fx:V7Pin.ping/1", "V7Pin", "/x"},
    {"fx:V8OneLineBlock.ping/0", "V8OneLineBlock", "/x"},
    {"fx:V9MultiClause.ping/1", "V9MultiClause", "/x"},
    {"fx:V9MultiClause.ping/1", "V9MultiClause", "/x"}
  ],
  "http9/write-1" => [
    {"fx:W1GetEnvDefault.ping/0", "https://api.stripe.com", "/v1/x"},
    {"fx:W2OrDefault.ping/0", "https://api.stripe.com", "/v1/x"},
    {"fx:W3VarOrDefault.ping/1", "W3VarOrDefault", "/x"},
    {"fx:W4Config.ping/0", "W4Config", "/x"}
  ]
}