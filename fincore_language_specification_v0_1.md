# FinCore — Техническа спецификация на езика, Go бекенда и самохостващия бутстрап компилатор (v0.1)

**Статус:** Изпълнима спецификация за имплементация (Draft v0.1 — Self-Hosting Go Backend Edition)

**Бутстрап стратегия:**
* **Stage 0 (`finc0`):** Бутстрап компилатор, написан на Go 1.22+ (`CGO_ENABLED=0`), без външни зависимости.
* **Stage 1+ (`finc1`, `finc2`, `finc3`):** Продукционен компилатор, написан изцяло на **FinCore** (чрез фиксирани арени и явни OS capabilities), компилиран до Go + `fincrt` и верифициран чрез детерминистична неподвижна точка (`stage2.go == stage3.go`).

**Целева платформа (v0.1):** Преносим статичен бинарен файл чрез Go (`CGO_ENABLED=0`, Go 1.22+, Linux `x86_64` / `aarch64`, macOS / BSD).

**Предназначение на документа:** Самостоятелен инженерен чертеж, достатъчен за пълна имплементация на `finc0` (Go), `fincrt` (Go), самохостващия компилатор `compiler/` (FinCore), тестовия пакет и бутстрап верификацията от разработчик или автономен кодиращ агент.

---

## Съдържание

1. [Визия, философия и инженерни цели](#1-визия-философия-и-инженерни-цели)
2. [Лексикална структура](#2-лексикална-структура)
3. [Пълна EBNF граматика](#3-пълна-ebnf-граматика)
4. [Типова система: три слоя и две класи стойности](#4-типова-система-три-слоя-и-две-класи-стойности)
5. [Слой 1: Интенсионални кодове (`Code n`, `El`) и горивен `comptime`](#5-слой-1-интенсионални-кодове-code-n-el-и-горивен-comptime)
6. [Слой 2: Проверка за тоталност, взаимна рекурсия с `depth` и границата `driver`](#6-слой-2-проверка-за-тоталност-взаимна-рекурсия-с-depth-и-границата-driver)
7. [Слой 3: Неявна $\Omega_N$-параметричност и проверка на обхвати (Scope Checker)](#7-слой-3-неявна-omega_n-параметричност-и-проверка-на-обхвати-scope-checker)
8. [Структурна конкуренция и Go Runtime (`fincrt`)](#8-структурна-конкуренция-и-go-runtime-fincrt)
9. [Архитектура на бутстрапа (`finc0` $\to$ `finc1` $\to$ `finc2`) и генериране на Go код](#9-архитектура-на-бутстрапа-finc0-to-finc1-to-finc2-и-генериране-на-go-код)
10. [Спецификация на стандартната библиотека (v0.1 Core + Compiler Capabilities)](#10-спецификация-на-стандартната-библиотека-v01-core--compiler-capabilities)
11. [Каталог на компилаторните грешки](#11-каталог-на-компилаторните-грешки)
12. [Еталонна програма: TCP Echo сървър с Graceful Shutdown](#12-еталонна-програма-tcp-echo-сървър-с-graceful-shutdown)
13. [Приемателни, конформансни и бутстрап тестове (Conformance Suite)](#13-приемателни-конформансни-и-бутстрап-тестове-conformance-suite)
14. [Поетапен план за имплементация (Milestones M0–M8)](#14-поетапен-план-за-имплементация-milestones-m0m8)

---

## 1. Визия, философия и инженерни цели

### 1.1. Основна теза

**FinCore** е системен език за програмиране с **ергономиката на Go**, изграден върху принципите на **математическия финитизъм** (в духа на Норман Уайлдбъргър). Вместо да се опитва да изгради безкрайна семантика за „вселена, съдържаща себе си“ (`Type : Type`), актуални безкрайности или тежък borrow-checker с анотации на животи, FinCore превръща трите фундаментални логико-комбинаторни граници на типовите системи в три прости, статично проверими и практически полезни правила на компилатора:

| Математическа граница | Класическият проблем | Инженерно правило в **FinCore** |
| :--- | :--- | :--- |
| **Граница 1: Парадокс на Жирар–Хъркенс** | Наивното тотално `Type : Type` населява празния тип $\emptyset$ чрез краен $\lambda$-терм. | Твърдо разделение между **тотални функции** (`func` с намаляваща крайна мярка), **горивен `comptime`** (краен бюджет от стъпки) и **външен ОС слой** (`driver func` / `driver for`). |
| **Граница 2: Канторова пропускливост ($B_{k+1} > k$)** | $k$ кода не могат да представят всички екстенсионални PER-ове ($B_{k+1}$) на същото ниво без забавяне $\triangleright$. | **Интенсионални кодове** `Code n` / `El` (кодираме краен синтаксис, стратифициран по $n \in \mathbb{N}$) и **статично ограничен капацитет** (`[MaxConns]`, `NewScope(MaxConns)`, фиксирани арени за самия компилатор). |
| **Граница 3: Двойна категория и $\Omega_N$-параметричност** | Равномерността (равенство/топология) и параметричността (свободни теореми) не бива да се сливат в едно симетрично изображение. | **Второкласни (scoped) ресурси** (`Conn`, `Listener`, `Scope`, `FS`, `Exec`) с неявна ранг-2 параметричност (забрана за бягство извън `scope`) и непрозрачен полиморфизъм без runtime reflection върху типови параметри. |

### 1.2. Архитектурно решение за v0.1: Go бекенд + самохостване (Self-Hosting)

Компилаторът се изгражда на **два последователни етапа**:

1. **Stage 0 (`finc0`, написан на Go):** Пълна референтна имплементация на FinCore v0.1 на чист Go 1.22+ (`bootstrap/`). Позволява бърза, безпогрешна проверка на езика и компилиране както на приложни програми (`examples/tcp_echo.fc`), така и на самия FinCore компилатор.
2. **Stage 1+ (`finc1` / `finc2`, написан на FinCore):** Пълна имплементация на компилатора на **самия FinCore** (`compiler/`), използваща **Data-Oriented Design** — фиксирани статични арени (`TokenArena`, `NodeArena`, `StringPool`, `OutBuf`) и 32-битови индекси (`NodeID`, `SymID`) вместо динамични рекурсивни указатели на heap-а.
3. **Ултимативният тест на финитисткия дизайн:** Самохостването доказва на практика, че език без неограничени `make`/`append`/`map`, без `nil` ресурси и с тотален слой `func` е напълно способен да напише реален, сложен системен софтуер (собствения си компилатор!).
4. **Go като целеви код (v0.1):** И `finc0`, и `finc1`/`finc2` генерират чист, детерминистичен Go код, свързват го с `runtime/fincrt` (~250 реда Go) и произвеждат статичен ELF с `CGO_ENABLED=0 go build`. В бъдеща версия v0.2 самохостващият компилатор може да добави втори бекенд (`--target=c11` или `--target=zig`) без промяна в езика.

### 1.3. Ергономични и системни цели

1. **Ергономика на Go:** плосък поток на управление с `(T, error)`, `if err != nil`, `defer`, `:=`, методи върху структури (`func (p *Parser) parseExpr(...)`), без `<'a>` лайфтайм анотации.
2. **Ограничена приложна памет:** FinCore програмите (включително самият самохостващ компилатор!) работят с изрично зададени крайни капацитети — без неограничени `append`/`make`/`new` алокации в приложния код.
3. **Няма `nil` паники за ресурси:** първокласните данни имат нулеви стойности (`var arena CompilerArena`), но живите системни ресурси (`Conn`, `Listener`, `Scope`, `Context`, `FS`, `Exec`) **нямат `nil` състояние** и се получават единствено чрез явни capabilities.
4. **Нула ambient authority:** Сигнатурата на `main` декларира точно какви системни способности ползва програмата — например `tcp_echo.fc` приема `(net Net, sig Signals)`, докато компилаторът `finc` приема `(fs FS, env Env, io Stdio, exec Exec)` и изобщо няма достъп до мрежата.
5. **Структурна конкуренция по дизайн:** няма свободно `go f()`. Всяка конкурентна задача принадлежи на ограничен `scope` с фиксиран таван и гарантирано завършване преди напускане на блока.

### 1.4. Изрични Non-Goals (какво НЕ се имплементира в v0.1)

* Няма неограничени динамични алокации в езика (`make`, `new`, `append`, `map` не са част от v0.1; компилаторът ползва фиксирани арени и хеш таблици с отворено адресиране).
* Няма рекурсивни `struct` типове чрез heap указатели (AST дърветата се представят чрез целочислени индекси `NodeID` в `NodeArena`).
* Няма интерфейси с динамично диспечиране (`vtable`) и няма runtime reflection (`reflect`).
* Няма borrow-checker с нелексикални животи (използва се простият **Scope/Depth Checker** от Раздел 7).
* Няма C11/Zig/LLVM бекенд в v0.1 (цели се единствено Go 1.22+ бекенд).
* Няма SMT солвър (като Z3) — проверката за тоталност използва лек, детерминистичен анализатор (Раздел 6).

---

## 2. Лексикална структура

### 2.1. Кодировка и бели пространства

* Изходните файлове имат разширение `.fc` и са кодирани в UTF-8.
* Бели пространства: `' '`, `'\t'`, `'\r'`, `'\n'`.
* Коментари:
  * Едноредови: `// ...` до края на реда.
  * Многоредови: `/* ... */` (не се влагат).

### 2.2. Автоматично вмъкване на точки и запетаи (Go правило)

Лексерът **автоматично вмъква `;`** при срещане на `'\n'`, ако последният токен на реда е един от:
* идентификатор (`IDENT`);
* литерал (`INT_LIT`, `STRING_LIT`, `CHAR_LIT`, `true`, `false`, `nil`);
* ключовите думи `break`, `continue`, `return`;
* операторите `++`, `--`;
* затварящи скоби `)`, `]`, `}`.

Следствие: отварящата скоба `{` на `func`, `if`, `for`, `scope`, `struct`, `enum`, `match` **задължително** стои на същия ред като заглавието.

### 2.3. Ключови думи (Keywords)

Запазени думи (27 броя):

```text
package   import    const     type      var       func
driver    comptime  scope     for       if        else
return    break     continue  defer     struct    enum
match     true      false     nil       any       decreases
```

### 2.4. Вградени типове и идентификатори (Predeclared Universe)

* **Скаларни първокласни типове (`Value`):** `bool`, `u8`, `u16`, `u32`, `u64`, `i8`, `i16`, `i32`, `i64`, `int`, `usize`, `byte` (псевдоним на `u8`), `string` (неизменяем срез от байтове), `error`, `Duration`, `Signal`, `ExitStatus`.
* **Вградени типови функции (Слой 1):** `Code`, `El`.
* **Вградени второкласни типове (Capabilities & Resources — `Scoped` / `Linear`):**
  * Мрежа и конкуренция: `Net`, `Signals`, `Clock`, `Listener`, `Conn`, `Context`, `CancelFunc`, `Scope`, `Slot`.
  * OS и компилаторни capabilities (за самохостване): `FS`, `Env`, `Stdio`, `Exec`.
* **Вградени функции (тотални):** `len`, `cap`, `copy`, `bytesEqual`, `strBytes`, `bytesToStr`, `formatUint`, `parseUint`, `NewContext`, `NewScope`.
* **Вградена `panic`:** позволена само в `driver` или като недостижима ветка.

### 2.5. Оператори и пунктуация

```text
+    -    *    /    %    &    |    ^    <<   >>
==   !=   <    <=   >    >=   &&   ||   !
=    :=   +=   -=   *=   /=   ++   --
(    )    [    ]    {    }    ,    .    :    ;    ->   ...
```

### 2.6. Литерали

* Целочислени: десетични (`1024`, `10_000`, `0`), шестнадесетични (`0xFF`), двоични (`0b1010`). Долното тире `_` е позволено като визуален разделител.
* Низови: `"0.0.0.0:8080"` с escape последователности `\n`, `\r`, `\t`, `\\`, `\"`, `\xHH`.
* Символни: `'a'`, `'\n'` (тип `byte`).

---

## 3. Пълна EBNF граматика

Граматиката е LL(1) с lookahead 2 (подходяща за чист recursive-descent парсер както в Go `finc0`, така и в самохостващия FinCore `finc1`). Поддържа **многофайлови пакети** (всички `.fc` файлове в една директория с еднакъв `package` образуват един пакет), **локални импорти** и **методи върху структури**.

```ebnf
SourceFile      = PackageClause ";" { ImportDecl ";" } { TopLevelDecl ";" } EOF .
PackageClause   = "package" IDENT .
ImportDecl      = "import" ( ImportSpec | "(" { ImportSpec ";" } ")" ) .
ImportSpec      = [ IDENT ] STRING_LIT .

TopLevelDecl    = ConstDecl
                | TypeDecl
                | VarDecl
                | FuncDecl .

(* Декларации *)
ConstDecl       = "const" ( ConstSpec | "(" { ConstSpec ";" } ")" ) .
ConstSpec       = IDENT [ Type ] "=" Expr .

TypeDecl        = "type" IDENT [ TypeParams ] "=" Type
                | "type" IDENT [ TypeParams ] Type .

VarDecl         = "var" IDENT ( Type [ "=" Expr ] | "=" Expr ) .

(* Функции и методи (с опционален receiver `(r *T)` или `(r T)`) *)
FuncDecl        = [ FuncModifier ] "func" [ Receiver ] IDENT [ TypeParams ] Signature
                  [ DecreasesClause ] Block .
FuncModifier    = "driver" | "comptime" .
Receiver        = "(" IDENT [ "*" ] IDENT [ "[" IdentList "]" ] ")" .

TypeParams      = "[" TypeParamList "]" .
TypeParamList   = TypeParam { "," TypeParam } .
TypeParam       = IDENT { "," IDENT } TypeConstraint .
TypeConstraint  = "any" | "usize" | Type .

Signature       = "(" [ ParamList ] ")" [ ResultType ] .
ParamList       = ParamDecl { "," ParamDecl } [ "," ] .
ParamDecl       = IDENT { "," IDENT } Type .
ResultType      = Type | "(" Type { "," Type } ")" .

DecreasesClause = "decreases" "(" Expr ")" .

(* Типове *)
Type            = NamedType
                | ArrayType
                | SliceType
                | PointerType
                | StructType
                | EnumType
                | FuncType
                | CodeType
                | ElType .

NamedType       = IDENT [ "." IDENT ] [ "[" TypeOrExpr { "," TypeOrExpr } "]" ] .
TypeOrExpr      = Type | Expr .
ArrayType       = "[" Expr "]" Type .
SliceType       = "[" "]" Type .
PointerType     = "*" Type .

StructType      = "struct" "{" { FieldDecl ";" } "}" .
FieldDecl       = IDENT { "," IDENT } Type .

EnumType        = "enum" "{" { VariantDecl ";" } "}" .
VariantDecl     = IDENT [ "(" Type { "," Type } ")" ] .

FuncType        = [ "driver" ] "func" Signature .
CodeType        = "Code" Expr .                 (* ниво n : Nat, напр. Code 0 *)
ElType          = "El" "(" Expr ")" .           (* декодиране на comptime код *)

(* Блокове и инструкции *)
Block           = "{" { Stmt ";" } "}" .

Stmt            = ConstDecl
                | VarDecl
                | ShortVarDecl
                | AssignStmt
                | IncDecStmt
                | ExprStmt
                | IfStmt
                | MatchStmt
                | ForStmt
                | ScopeStmt
                | DeferStmt
                | ReturnStmt
                | BreakStmt
                | ContinueStmt
                | Block .

ShortVarDecl    = IdentList ":=" ExprList .
AssignStmt      = ExprList AssignOp ExprList .
AssignOp        = "=" | "+=" | "-=" | "*=" | "/=" .
IncDecStmt      = Expr ( "++" | "--" ) .
ExprStmt        = Expr .

IfStmt          = "if" [ SimpleStmt ";" ] Expr Block [ "else" ( IfStmt | Block ) ] .
SimpleStmt      = ShortVarDecl | AssignStmt | IncDecStmt | ExprStmt .

MatchStmt       = "match" Expr "{" { MatchArm ";" } "}" .
MatchArm        = Pattern "->" ( Expr | Block ) .
Pattern         = "_" | IDENT [ "." IDENT ] [ "(" IdentList ")" ] | Literal .

(* Цикли: обикновен тотален `for` срещу външен `driver for` *)
ForStmt         = [ "driver" ] "for" [ ForHeader ] [ DecreasesClause ] Block .
ForHeader       = Expr
                | [ SimpleStmt ] ";" [ Expr ] ";" [ SimpleStmt ] .

(* Структурна конкуренция *)
ScopeStmt       = "scope" IDENT ":=" Expr Block .

DeferStmt       = "defer" Expr .
ReturnStmt      = "return" [ ExprList ] .
BreakStmt       = "break" .
ContinueStmt    = "continue" .

(* Изрази по нарастващ приоритет *)
Expr            = LogicalOr .
LogicalOr       = LogicalAnd { "||" LogicalAnd } .
LogicalAnd      = Equality { "&&" Equality } .
Equality        = Relational { ( "==" | "!=" ) Relational } .
Relational      = Additive { ( "<" | "<=" | ">" | ">=" ) Additive } .
Additive        = Multiplicative { ( "+" | "-" | "|" | "^" ) Multiplicative } .
Multiplicative  = Unary { ( "*" | "/" | "%" | "&" | "<<" | ">>" ) Unary } .
Unary           = ( "!" | "-" | "&" | "*" ) Unary | Postfix .

Postfix         = Primary { CallSuffix | IndexOrSlice | FieldAccess } .
CallSuffix      = "(" [ ExprList [ "," ] ] ")" .
IndexOrSlice    = "[" [ Expr ] [ ":" [ Expr ] ] "]" .
FieldAccess     = "." IDENT .

Primary         = Literal
                | IDENT [ "[" TypeOrExpr { "," TypeOrExpr } "]" ]
                | "(" Expr ")"
                | FuncLit
                | StructLit .

FuncLit         = [ "driver" ] "func" Signature Block .
StructLit       = NamedType "{" [ FieldInit { "," FieldInit } [ "," ] ] "}" .
FieldInit       = IDENT ":" Expr .

Literal         = INT_LIT | STRING_LIT | CHAR_LIT | "true" | "false" | "nil" .
IdentList       = IDENT { "," IDENT } .
ExprList        = Expr { "," Expr } .
```

**Забележки към граматиката:**
* **Литералът `nil`:** валиден е **единствено** за стойности от тип `error` и за празен срез `[]T`. Присвояването на `nil` на указател `*T` или на `Scoped` ресурс е грешка `E2004`.
* **Типови преобразувания (casts):** за скаларни типове важи стандартният синтаксис за извикване `u32(x)`, `usize(n)`, `byte(ch)`, `int(k)` (парсира се като `CallSuffix` и се разпознава в Type Checker-а).
* **Локални импорти:** `import "compiler/lexer"` зарежда пакета от директорията `compiler/lexer` спрямо корена на проекта (с детерминистично сортиране на файловете по име). Цикличните импорти са грешка `E2006`.

---

## 4. Типова система: три слоя и две класи стойности

### 4.1. Класификация на типовете

Всеки тип $T$ в FinCore принадлежи на точно една от двете класи:

1. **Клас `Value` (Първокласни данни):**
   * Включва: скалари (`bool`, `u8..u64`, `i8..i64`, `int`, `usize`, `byte`, `string`, `error`, `Duration`, `Signal`, `ExitStatus`), фиксирани масиви `[N]T` (ако `T : Value`), срезове `[]T` (ако `T : Value`), `struct` и `enum`, чиито всички полета са `Value` (включително фиксирани арени като `type Vec[N usize, T any] struct { data [N]T; len usize }`), и `Code n`.
   * Свойства: могат да се копират, да се връщат от функции, да се пазят в глобални `const`/`var`, имат добре дефинирана нулева стойност (всички битове `0`).
   * **Указатели `*T`:** указател към `Value` (напр. `*[BufSize]byte`, `&pool`, `*Parser`) наследява **обхвата (`origin_depth`)** на променливата, чийто адрес е взет, и се проверява като второкласен (не може да надживее сочената променлива).

2. **Клас `Scoped` (Второкласни ресурси и Capabilities):**
   * Включва: `Net`, `Signals`, `Clock`, `FS`, `Env`, `Stdio`, `Exec`, `Listener`, `Conn`, `Context`, `CancelFunc`, `Scope`, `Slot`, както и всеки `struct`, съдържащ поне едно `Scoped` или указателно `*T` поле (напр. `type Parser struct { arena *NodeArena; pos usize }`).
   * Подклас **`Linear` (линейни ресурси):** `Listener`, `Conn`, `Slot`, `CancelFunc`. Тези типове изискват точно едно финализиране (`Close()`, `ReleaseSlot()`, извикване на `CancelFunc`, или прехвърляне на собствеността чрез `conns.Go(slot, ...)`).
   * Свойства на `Scoped`:
     * **Нямат нулева стойност:** `var c Conn` без инициализация е грешка `E2005`.
     * **Не могат да се копират** (за `Linear`): присвояването или подаването по стойност извършва **move** (прехвърляне на собственост), освен при извикване на метод върху приемника (`conn.Read(...)` заема временно за продължителността на извикването).
     * **Не могат да бягат нагоре по стека** (виж Раздел 7).

### 4.2. Проверка на типовете (Typing Judgments)

Типовата среда има вида $\Gamma; \Delta; \ell; \mu \vdash e : \tau$, където:
* $\Gamma$ съпоставя променливите на техните типове, нива на обхват `(x : T @ d)` и статус на линейност (`Live | Moved | DeferredClose`).
* $\Delta$ съхранява активните `comptime` константи и типови/const параметри (`[N usize, T any]`).
* $\ell \in \mathbb{N}$ е текущата лексикална дълбочина на обхвата (`scope depth`), започваща от $\ell = 0$ на ниво пакет и $\ell = 1$ в тялото на `main`.
* $\mu \in \{\text{Comptime}, \text{Total}, \text{Driver}\}$ е текущият режим на ефекти.

**Присвояемост (както в Go, решение 126 в `docs/semantics_decisions.md`):** стойност от тип `V` може да се присвои на `T`, ако `V` и `T` са идентични, или имат идентични базови (underlying) типове и поне един от тях **не е именуван**; предварително декларираните типове `u8`…`usize`, `bool`, `string`, `error` се броят за именувани. Следователно при `type Name u32` стойност `Name` не може да се подаде като `u32` (и обратно) без явно преобразуване `u32(n)` / `Name(x)` — грешка `E2002`. Нетипизирани константи, `nil`, Total→`driver` функции и константни низове като `[]byte` следват отделните си правила.

### 4.3. Множествени връщани стойности, методи и `error`

* Функциите и методите могат да връщат кортеж `(T1, T2, ..., error)`.
* Метод `func (p *T) m(args) Res` е синтактична захар за функция, чийто първи параметър `p : *T` е заем на обхвата на извикващия (`p` не може да избяга през `return` или глобална променлива).
* Празната променлива `_` изхвърля `Value` стойност (вкл. `error`), но **не може** да изхвърли `Linear` ресурс (`_ = conn` е грешка `E3004`).

---

## 5. Слой 1: Интенсионални кодове (`Code n`, `El`) и горивен `comptime`

### 5.1. Как се заобикаля Граница 2 (Канторова пропускливост)

Вместо `Type : Type` или безкрайни йерархии от вселени с полиморфизъм по нива, FinCore предоставя **интенсионална алгебра от кодове `Code n`** за всяко крайно $n \in \mathbb{N}$.

Вградени конструктори на `Code n` (достъпни в `comptime`):

```text
CodeEmpty()                         : Code n
CodeUnit()                          : Code n
CodeBool()                          : Code n
CodeU8() .. CodeU64()               : Code n
CodeI8() .. CodeI64()               : Code n
CodeArray(len: usize, elem: Code n) : Code n
CodeSlice(elem: Code n)             : Code n
CodeStruct(fields: []FieldCode n)   : Code n
CodeUniv(n: usize)                  : Code (n + 1)
```

**Правило за стратификация (твърда проверка):**
* `CodeUniv(n)` има строго тип `Code (n + 1)`.
* `El(CodeUniv(n))` се редуцира до типа `Code n`.
* Опитът да се подаде `CodeUniv(n)` на функция, очакваща `Code n`, се отхвърля със статична грешка `E2010: UNIVERSE_LEVEL_MISMATCH`.

### 5.2. Приложение в самохостващия компилатор

В `compiler/` Слой 1 не е декоративна функция — той задава крайните капацитети на самия компилатор при компилация (например буферите за входен код, токени, AST възли и изходен Go код):

```go
const (
	MaxSourceBytes = 4 * 1024 * 1024
	MaxOutputBytes = 8 * 1024 * 1024
)

comptime func ByteBufCode(cap usize) Code 0 {
	return CodeArray(cap, CodeU8())
}

type SourceBuf = El(ByteBufCode(MaxSourceBytes))
type EmitBuf   = El(ByteBufCode(MaxOutputBytes))
```

### 5.3. Горивен `comptime` оценител

Всеки израз в позиция на `const`, размер на масив `[Expr]T`, `Code Expr`, `El(Expr)` или извикване на `comptime func` се оценява от интерпретатора в Фаза 4:
* **Горивен параметър:** `fuel : u64` (по подразбиране `1_000_000`, конфигурируем с CLI флаг `--comptime-fuel=N`).
* **Правило за разход на гориво:** всяка итерация на цикъл, всяко функционално извикване и всяко индексиране намалява `fuel` с $1$.
* При `fuel == 0` оценителят незабавно спира с `E4001: COMPTIME_OUT_OF_FUEL` и точния стек на извикванията.
* Резултатът от `El(...)` се сгъва до конкретен Go тип преди кодогенерацията (например `[4194304]uint8`), така че в генерирания Go код не остава никакъв `comptime` runtime.

---

## 6. Слой 2: Проверка за тоталност, взаимна рекурсия с `depth` и границата `driver`

### 6.1. Йерархия на режимите на ефекти

Режимите образуват линейна наредба:

$$\text{Comptime} \prec \text{Total} \prec \text{Driver}$$

**Правило за извикване (Effect Containment):**
* Код в режим $\mu_1$ може да извиква функция в режим $\mu_2$ тогава и само тогава, когато $\mu_2 \preceq \mu_1$.
* `func` (тотална) може да извиква само `func` и `comptime func`. Извикване на `driver func` или използване на `driver for` вътре в `func` поражда грешка `E5001: DRIVER_CALL_IN_TOTAL`.
* `main` е единствената входна точка и приема подмножество от вградените capabilities (`Net`, `Signals`, `Clock`, `FS`, `Env`, `Stdio`, `Exec`).

### 6.2. Алгоритъм за проверка на тоталност (Totality Checker)

Всяка функция без модификатор `driver` трябва да бъде доказано тотална. Компилаторът проверява **всички `for` цикли** и **всички цикли в графа на извикванията (Call Graph SCCs)** чрез 4 детерминистични правила без SMT солвър:

#### Правило T1: Структурно броене нагоре/надолу (`for i := a; i < b; i++`)
Цикъл `for i := init; i < bound; i += step` (или `i++`) е валиден, ако `bound` е инвариантен в тялото, `step > 0` е положителна константа и `i` не се намалява (нито му се взима `&i`). Аналогично за броене надолу `for i := init; i > bound; i -= step`.
*Практическо следствие:* обхождането на фиксирани арени (`for i := 0; i < arena.len; i++`) и линейното търсене в хеш таблици (`for step := 0; step < MaxBuckets; step++`) са автоматично тотални.

#### Правило T2: Строго скъсяване на срез (`Slice Shrink Idiom`)
Цикълът `for rem := data; len(rem) > 0; { ... }` е валиден, ако по **всеки път в контролния граф (CFG)**, достигащ края на тялото или `continue`:
1. `rem` се обновява с `rem = rem[k:]` или `rem = rem[:len(rem)-k]`;
2. по този път е доказано `k > 0` (положителна константа ИЛИ предхождаща проверка `if k == 0 { return/break }` за беззнаково `k : usize`);
3. `rem` не се удължава и не му се взима адресът `&rem`.

#### Правило T3: Явна клауза `decreases(M)` — директна И взаимна рекурсия
За да може самохостващият компилатор да напише чист recursive-descent парсер (`parseExpr` $\leftrightarrow$ `parsePrimary` $\leftrightarrow$ `parseStmt`) и AST обхождания изцяло в **тоталния слой `func`**, Правило T3 поддържа както директна, така и **взаимна рекурсия по силно свързани компоненти (SCC) в графа на извикванията**:

1. **За `for` цикъл:** `decreases(M)` (където `M : usize` или `len(s)`) трябва синтактично да намалява ($M' < M$) по всеки път към следваща итерация (напр. `hi - lo` при бинарно търсене или `pos` при лексер с `decreases(len(src) - pos)`).
2. **За директна и взаимна рекурсия (SCC проверка):**
   * Изчисляват се силно свързаните компоненти (Tarjan SCC) на графа на извикванията между `func` функциите в пакета.
   * За всяка нетривиална SCC (функция, която вика себе си, или група взаимно рекурсивни функции $\{f_1, \dots, f_m\}$), **всяка функция $f_i$ в компонентата** трябва да има клауза `decreases(M_i)` от тип `usize`.
   * За всяко ребро $f_i \to f_j$ вътре в SCC, аргументът на мярката $M_j$ при извикването трябва синтактично да е строго по-малък от $M_i$ (вкл. `depth - 1` след предхождаща проверка `if depth == 0 { return ..., ErrMaxDepth }`).
   * *Бонус гаранция:* Това едновременно доказва терминиране на парсера и Type Checker-а **и предотвратява stack overflow** при злонамерено дълбоко вложен вход!

```go
// Пример от самохостващия парсер: взаимна рекурсия с доказано терминиране
func (p *Parser) parseExpr(depth usize) (NodeID, error) decreases(depth) {
	if depth == 0 {
		return 0, ErrMaxDepth
	}
	return p.parsePrimary(depth - 1)
}

func (p *Parser) parsePrimary(depth usize) (NodeID, error) decreases(depth) {
	if depth == 0 {
		return 0, ErrMaxDepth
	}
	// ... при '(' извиква p.parseExpr(depth - 1) ...
	return p.parseExpr(depth - 1)
}
```

#### Правило T4: Отхвърляне
Всеки `for` цикъл или SCC в тоталния слой, за който нито T1, нито T2, нито T3 успяват, се отхвърля с `E4002: UNPROVEN_TERMINATION` (за цикли/директна рекурсия) или `E4004: MUTUAL_RECURSION_NO_MEASURE` (за SCC без обща намаляваща мярка).

---

## 7. Слой 3: Неявна $\Omega_N$-параметричност и проверка на обхвати (Scope Checker)

### 7.1. Формални правила на Scope Checker-а

Всяка променлива `x` в контекста $\Gamma$ има целочислена дълбочина $\text{depth}(x) \in \mathbb{N}$:
* Глобални променливи на ниво пакет: $\text{depth} = 0$.
* Параметри и локални променливи в `func`: $\text{depth} = \ell_{\text{fn}}$.
* Влизане в блок `scope s := NewScope(K) { ... }` увеличава текущата дълбочина с $+1$: $\ell' = \ell + 1$.
* Всяка променлива от клас `Scoped` (или указател `&v`, или структура, съдържаща указател като `Parser{arena: &arena}`), създадена на дълбочина $\ell$, получава атрибут `origin_depth = l`.

**Петте инварианта (линейно време $O(\text{AST})$):**

1. **S1 (Без глобални ресурси):** Глобална променлива (`depth == 0`) не може да има тип `Scoped` или указател `*T` (грешка `E3001`).
2. **S2 (Само надолу по стека — Downward Assignment Rule):** Присвояване `lhs = rhs` е валидно само ако за `Scoped` или указател `rhs` е изпълнено:

   $$\text{origin\_depth}(\text{rhs}) \le \text{depth}(\text{lhs})$$

   *Пример:* `buf` от `conns.Go` (дълбочина 3) НЕ може да се запише в променлива от `main` (дълбочина 1), защото $3 \not\le 1$ (грешка `E3002: SCOPE_ESCAPE`).
3. **S3 (Забрана за връщане на по-вътрешен ресурс):** `return expr` не може да връща `Scoped` стойност или указател с $\text{origin\_depth}(\text{expr}) \ge \ell_{\text{fn}}$, освен ако функцията не е вграден **конструктор на ресурс** (`net.ListenTCP`, `ln.Accept`, `conns.AcquireSlot`, `NewContext`, `ctx.WithTimeout`, `sig.NotifyContext`). Поле на указателен receiver `p *T` не може да запазва по-вътрешен указател в `*p`.
4. **S4 (Замикания в `conns.Go`):** Анонимната функция `driver func(buf *T) { ... }`, подадена на `conns.Go(slot, fn)`, може да захваща:
   * `Value` променливи (копират се по стойност);
   * `Scoped` стойности с $\text{origin\_depth} \le \text{depth}(\text{conns})$ (`drainCtx`, `hardCtx`, `&pool`);
   * `Linear` ресурси (`conn`), които се **прехвърлят (move)** в задачата — след `conns.Go(...)` променливата `conn` става `Moved` във външния цикъл (`E3003: USE_AFTER_MOVE`).
5. **S5 (Линейно приключване на `Linear` ресурси):** За всяка променлива `r` от подклас `Linear` (`Conn`, `Listener`, `Slot`, `CancelFunc`), по **всеки изходен път** от блока, в който е дефинирана, нейното състояние трябва да е `Moved` или `Consumed` (чрез пряко финализиране или `defer`). Неизпълнението поражда `E3005: LINEAR_RESOURCE_LEAK`.

### 7.2. Полиморфизъм и свободни теореми

За всяка полиморфна функция `func f[T any](...)`:
* В тялото на `f` са забранени type assertions, type switches, `sizeof(T)` и сравнение на типове (`E2011: NON_PARAMETRIC_OP`).
* В генерирания Go код полиморфните функции се транслират директно към Go generics (`func f[T any](...)`) — без `any` боксинг и без reflection.

---

## 8. Структурна конкуренция и Go Runtime (`fincrt`)

### 8.1. Семантика на `scope`

Конструкцията `scope conns := NewScope(MaxConns) { ... }` има следната операционна семантика в `fincrt`:

1. Създава `*fincrt.Scope` с фиксиран таван `maxTasks = MaxConns`, буфериран канал от свободни слот-индекси `free := make(chan int, maxTasks)` (предварително запълнен с `0, 1, ..., maxTasks-1`), `sync.WaitGroup`, `mu sync.Mutex`, `active int` и `idleCh chan struct{}` (затворен при `active == 0`, пресъздаван при преход `0 -> 1`).
2. `fincrt.AcquireSlot(conns, ctx, pool[:])`:
   * Проверява първо `if ctx.Done() { return Slot[T]{}, ErrCancelled }` (приоритет на отмяната пред Go `select` недетерминизма).
   * След това изпълнява `select` между `<-ctx.DoneChan()` и `idx := <-s.free`.
   * Връща `Slot[T]{idx: idx, ptr: &pool[idx]}`.
3. `fincrt.ReleaseSlot(conns, slot)`: връща `slot.idx` в `s.free`.
4. `fincrt.Go(conns, slot, fn)`: увеличава `active` + `wg.Add(1)` и стартира горутина, чийто `defer` връща `slot.idx` в `s.free`, намалява `active` (затваряйки `idleCh` при достигане на `0`) и вика `wg.Done()`.
5. `conns.WaitTimeout(d Duration) bool`: използва `time.NewTimer` (с `defer timer.Stop()`) и `select` върху `<-s.idleChan()` и `<-timer.C` — **0 изтекли горутини** и 100% чистота под `go test -race`.
6. **Неявна бариера при изход от `scope`:** компилаторът вмъква `conns.Wait()` при всеки изход от блока.

### 8.2. Прекъсваем I/O в `fincrt` (решаване на клопката с `net.Conn`)

Тъй като Go `net.Conn.Read`/`Write` и `net.TCPListener.Accept` не приемат `context.Context`, `fincrt` задължително прилага шаблона с `context.AfterFunc` (Go 1.21+):

1. **`fincrt.Context` без теч на таймери в цикли:**
   ```go
   type Context struct {
       base     context.Context
       deadline time.Time // нулева стойност = без deadline
   }
   ```
   `c.WithTimeout(d Duration) Context` смята `min(c.deadline, time.Now().Add(d))` изцяло на стека — **0 алокации и 0 изтекли таймери** в `driver for` цикли.
2. **Прекъсваемо `Conn.Read(ctx Context, buf []byte) (uint, error)`:**
   * Ако `ctx.Done()`, връща `(0, ctx.Err())`.
   * Извиква `c.raw.SetReadDeadline(ctx.deadline)`.
   * Регистрира `stop := context.AfterFunc(ctx.base, func() { _ = c.raw.SetReadDeadline(time.Now()) })` с `defer stop()`.
   * Превежда грешките точно: `ctx.base.Err() != nil` $\to$ `ErrCancelled`; `io.EOF` $\to$ `ErrEOF`; `net.Error.Timeout()` $\to$ `ErrTimeout`; иначе `ErrIO`.
3. **Прекъсваемо `Conn.Write` и `Listener.Accept`:** прилагат идентичен `SetWriteDeadline`/`SetDeadline` + `context.AfterFunc` шаблон.
4. **Идемпотентен `Close()`:** `Conn` и `Listener` пазят `closed *atomic.Bool`, така че изрично `_ = ln.Close()` преди `defer ln.Close()` е безопасно no-op.

### 8.3. OS Capabilities в `fincrt` (за самохостващия компилатор)

За да може `finc1` (написан на FinCore) да чете `.fc` файлове, да пише `.go` файлове и да стартира `go build`, `fincrt` предоставя 4 леки capability структури (виж пълните сигнатури в Раздел 10.6):
* `fincrt.FS` — четене в подаден буфер (`ReadFileInto`), запис (`WriteFile`), списък на `.fc` файлове в директория (сортиран лексикографски!), създаване/триене на временни директории.
* `fincrt.Env` — достъп до `os.Args` и `os.LookupEnv`.
* `fincrt.Stdio` — запис към `os.Stdout` и `os.Stderr`.
* `fincrt.Exec` — стартиране на външен процес (`os/exec.CommandContext`) с `Context` за отмяна (използва се единствено за извикване на `go build` / `go test`).

---

## 9. Архитектура на бутстрапа (`finc0` $\to$ `finc1` $\to$ `finc2`) и генериране на Go код

### 9.1. Триетапен бутстрап и критерий за неподвижна точка (Fixed-Point Gate)

```text
[bootstrap/ (Go)] ──(go build)──► finc0 (Stage 0 бинарен файл)
                                    │
                                    ▼  finc0 build --emit-go=stage1.go -o finc1 ./compiler
                                  finc1 (Stage 1 бинарен файл, написан на FinCore!)
                                    │
                                    ▼  finc1 build --emit-go=stage2.go -o finc2 ./compiler
                                  finc2 (Stage 2 бинарен файл, самокомпилиран!)
                                    │
                                    ▼  finc2 build --emit-go=stage3.go -o finc3 ./compiler
                                  finc3 (Stage 3 верификация)

КРИТЕРИЙ ЗА УСПЕШЕН БУТСТРАП (FIXED POINT):
  1) sha256(stage2.go) == sha256(stage3.go)           (побитова идентичност на генерирания Go код!)
  2) sha256(echo_from_finc1.go) == sha256(echo_from_finc2.go)
  3) finc2 минава 100% от testdata/fail, testdata/pass и testdata/integration (вкл. tcp_echo.fc)
```

**Защо проверяваме `stage2.go == stage3.go`?**
`stage1.go` се генерира от Go имплементацията `finc0`, а `stage2.go` се генерира от FinCore имплементацията `finc1`. Дори при минимални разлики в имената на временни променливи между `finc0` и `finc1`, от `stage2` нататък компилаторът компилира *сам себе си* — затова равенството `stage2.go == stage3.go` (и `sha256(finc2) == sha256(finc3)` при `go build -trimpath`) е неоспоримото доказателство за пълна коректност и детерминизъм на самохостването.

### 9.2. Задължителни правила за детерминизъм (и в `finc0`, и в `finc1`)

За да е гарантирана неподвижната точка `stage2.go == stage3.go`:
1. **Нула недетерминизъм при обхождане на хеш таблици:** В `finc0` (Go) НИКОГА не се емитира Go код при итерация над Go `map` — декларациите и символите се пазят в подредени срезове (или се сортират лексикографски).
2. **Детерминистичен ред на файловете:** При компилиране на директория (`./compiler`), `.fc` файловете се сортират строго лексикографски по относителния им път.
3. **Детерминистични временни имена:** Временните идентификатори са строго номерирани по ред на обхождане (`_fc_t0`, `_fc_t1`, ...).
4. **Без времеви печати и случайни пътища:** Генерираният `.go` файл не съдържа текущ час, PID или име на временна директория.

### 9.3. Структура на хранилището

```text
fincore/
├── go.mod                        # module fincore (go 1.22, zero external deps)
├── bootstrap/                    # STAGE 0: Бутстрап компилатор на Go (`finc0`)
│   ├── cmd/finc0/main.go         # CLI: parse, check, build, run, test
│   └── internal/
│       ├── diag/                 # Диагностики, SourceSpan (file:line:col), E1xxx..E5xxx
│       ├── lexer/                # Токенизатор с Go-style автоматично ';'
│       ├── ast/                  # AST възли
│       ├── parser/               # Recursive-descent парсер по Раздел 3
│       ├── resolve/              # Резолюция на имена, пакети и вградени типове
│       ├── comptime/             # Слой 1: Горивен интерпретатор (const, Code n, El)
│       ├── types/                # Type Checker (Value, Scoped, Linear, методи)
│       ├── scope/                # Слой 3: Scope/Depth Escape & Linearity Checker
│       ├── totality/             # Слой 2: Effect & Totality Checker (T1, T2, T3+SCC)
│       ├── ir/                   # Typed IR (сгънати El типове, scope бариери)
│       └── codegen/golang/       # Детерминистичен генератор на Go код + go/format
├── compiler/                     # STAGE 1+: Самохостващ компилатор на FinCore (`finc`)
│   ├── main.fc                   # `driver func main(fs FS, env Env, io Stdio, exec Exec) error`
│   ├── arena.fc                  # Фиксирани арени: SourceBuf, TokenArena, NodeArena, SymTable, OutBuf
│   ├── diag.fc                   # Форматиране на грешки E1xxx..E5xxx в буфер
│   ├── lexer.fc                  # Тотален лексер с decreases(len - pos)
│   ├── parser.fc                 # Тотален recursive-descent парсер с decreases(depth)
│   ├── comptime.fc               # Слой 1: горивен интерпретатор за const, Code n, El
│   ├── checker.fc                # Резолюция + Type Checker + Scope Checker + Totality (SCC)
│   └── codegen.fc                # Детерминистичен Go кодогенератор в EmitBuf
├── runtime/fincrt/               # Споделен Go runtime пакет (~250 реда Go)
│   ├── context.go                # Context, CancelFunc, NewContext, стеков WithTimeout
│   ├── signals.go                # Signals, NotifyContext (SIGINT/SIGTERM)
│   ├── net.go                    # Net, Listener, Conn (AfterFunc прекъсваем I/O)
│   ├── scope.go                  # Scope, Slot[T], AcquireSlot, Go, WaitTimeout (0 leaks)
│   ├── os_caps.go                # FS, Env, Stdio, Exec capabilities за самохостване
│   ├── builtins.go               # bytesEqual, formatUint, parseUint, copy helpers
│   ├── errors.go                 # Sentinel грешки (ErrEOF..ErrArenaFull)
│   └── fincrt_test.go            # Unit тестове под `go test -race`
├── scripts/
│   └── bootstrap_verify.go       # Кроplatformен верификатор: M0..M8 + stage2==stage3 gate
├── examples/
│   └── tcp_echo.fc               # Еталонният TCP Echo сървър (Раздел 12)
└── testdata/
    ├── pass/                     # Позитивни тестове
    ├── fail/                     # Негативни тестове с очакван код (// ERROR E3002)
    └── integration/              # End-to-end тестове (TCP echo + SIGTERM + race + bootstrap)
```

### 9.4. Финитистка Data-Oriented архитектура на самохостващия `compiler/`

За да се компилира `compiler/` сам със строгите правила на FinCore (без неограничени алокации, без рекурсивни heap указатели, с доказана тоталност на всички аналитични фази), самохостващият компилатор използва следните крайни структури в `compiler/arena.fc`:

| Структура в `compiler/arena.fc` | Фиксиран капацитет (v0.1) | Предназначение |
| :--- | :--- | :--- |
| `SourceArena` | `MaxFiles = 64`, `MaxSourceBytes = 4 MB` | Суровите байтове на всички `.fc` файлове; низовете са `Span{file u16, off u32, len u32}` (0 копирания). |
| `TokenArena` | `MaxTokens = 262_144` | Плосък масив от токени; индексира се с `TokID = u32`. |
| `NodeArena` | `MaxNodes = 131_072`, `MaxListItems = 262_144` | Плосък масив от `AstNode` (tagged `struct`/`enum`); децата са `NodeID = u32` или диапазон `(listStart, listLen)` в `NodeList`. |
| `TypeArena` | `MaxTypes = 32_768`, `MaxFields = 65_536` | Канонични разрешени типове, индексирани с `TypeID = u32`. |
| `SymTable` | `MaxSyms = 32_768`, `MaxBuckets = 65_536` | Таблица на символите с отворено адресиране (linear probing с ограничен цикъл `for step := 0; step < MaxBuckets; step++`). |
| `EmitBuf` | `MaxOutputBytes = 8 MB` | Буфер за генерирания Go код (преди `fs.WriteFile`). |

Всички капацитети се проверяват при вмъкване и при запълване връщат чиста грешка `ErrArenaFull` (никакъв панически OOM или тихо препълване).

### 9.5. Ключови правила при генериране на Go код (валидни и за `finc0`, и за `finc1`)

1. **Съответствие на типовете:**
   * `u8`/`byte` $\to$ `uint8`, `u16` $\to$ `uint16`, `u32` $\to$ `uint32`, `u64` $\to$ `uint64`, `usize` $\to$ `uint`;
   * `i8`..`i64` $\to$ `int8`..`int64`, `int` $\to$ `int`, `bool` $\to$ `bool`, `string` $\to$ `string`, `error` $\to$ `error`;
   * `El(PoolCode(1024, 4096))` $\to$ `[1024][4096]uint8` (изцяло разрешен при компилация).
2. **Транслация на `scope conns := NewScope(N) { body }`:**
   * **НЕ** обвивайте `body` в IIFE `func() { ... }()` (за да работят `return`, `break` и `continue`).
   * Генерирайте лексикален блок с `defer conns.Wait()` и явен `conns.Wait()` при нормален изход и преди всеки `break`/`continue` към външен цикъл.
3. **Транслация на `driver` и `decreases`:**
   * След преминаване на Totality Checker, `driver func` става `func`, `driver for` става `for`, а `decreases(...)` отпада.
4. **Транслация на `AcquireSlot` / `ReleaseSlot` / `Go`:**
   * Поради липсата на генерични методи в Go, транслират се към пакетни функции:
     * `conns.AcquireSlot(ctx, &pool)` $\to$ `fincrt.AcquireSlot(conns, ctx, pool[:])`
     * `conns.ReleaseSlot(slot)` $\to$ `fincrt.ReleaseSlot(conns, slot)`
     * `conns.Go(slot, fn)` $\to$ `fincrt.Go(conns, slot, fn)`
5. **Транслация на `main`:**
   * `driver func main(...) error` се генерира като `func _fc_main(...) error`, а Go `func main()` инжектира точно поисканите capabilities (`fincrt.DefaultNet()`, `fincrt.DefaultSignals()`, `fincrt.DefaultFS()`, `fincrt.DefaultEnv()`, `fincrt.DefaultStdio()`, `fincrt.DefaultExec()`).
6. **Многофайлова компилация (единен Go пакет за v0.1):**
   * За простота и максимална съвместимост между `finc0` и `finc1`, `finc build ./dir` може да слее всички пакети на програмата в един генериран `main.go` (с префиксирани имена `_pkg_<name>_<ident>` за импортираните локални пакети), така че да не се налага генериране на сложна структура от Go подпакети.

### 9.6. CLI интерфейс (идентичен за `finc0` и `finc1`/`finc2`)

```text
finc parse [--dump-ast] <file.fc | dir>
finc check [--comptime-fuel=N] <file.fc | dir>
finc build [-o out_bin] [--emit-go=out.go] [--race] [--trimpath] [--comptime-fuel=N] <file.fc | dir>
finc run   [--race] [--comptime-fuel=N] <file.fc | dir> [-- args...]
finc test  <dir>
```

---

## 10. Спецификация на стандартната библиотека (v0.1 Core + Compiler Capabilities)

### 10.1. Време и грешки (`Value`)

```go
type Duration u64 // наносекунди

const (
	Nanosecond  Duration = 1
	Microsecond Duration = 1_000
	Millisecond Duration = 1_000_000
	Second      Duration = 1_000_000_000
)

const (
	ErrEOF       error // io.EOF
	ErrTimeout   error // изтекъл краен хоризонт / таймаут
	ErrCancelled error // отменен Context
	ErrZeroWrite error // сокетът върна 0 записани байта
	ErrClosed    error // операция върху затворен ресурс
	ErrIO        error // обща ОС I/O грешка
	ErrNotFound  error // липсващ файл или директория
	ErrMaxDepth  error // достигната максимална дълбочина (decreases(depth))
	ErrArenaFull error // изчерпан статичен капацитет на арена
	ErrExec      error // неуспешно изпълнение на подпроцес
)
```

### 10.2. Контекст и отмяна (`Scoped` / `Linear`)

```go
func NewContext() (Context, CancelFunc)
func (c Context) Done() bool
func (c Context) Err() error
func (c Context) WithTimeout(d Duration) Context // 0 алокации, стеков deadline
```

### 10.3. Сигнали (`Signals` — Capability, `Scoped`)

```go
type Signal u8
const (
	SIGINT  Signal = 2
	SIGTERM Signal = 15
)

func (s Signals) NotifyContext(sigs ...Signal) (Context, CancelFunc)
```

### 10.4. Мрежа (`Net`, `Listener`, `Conn`)

```go
func (n Net) ListenTCP(addr string) (Listener, error)

func (ln Listener) Accept(ctx Context) (Conn, error)
func (ln Listener) Addr() string
func (ln Listener) Close() error

func (c Conn) Read(ctx Context, buf []byte) (usize, error)
func (c Conn) Write(ctx Context, data []byte) (usize, error)
func (c Conn) CloseWrite() error
func (c Conn) Close() error
```

### 10.5. Структурна конкуренция (`Scope`, `Slot`)

```go
func NewScope(maxTasks usize) Scope
func (s Scope) AcquireSlot[N usize, T any](ctx Context, pool *[N]T) (Slot[T], error)
func (s Scope) ReleaseSlot[T any](slot Slot[T])
func (s Scope) Go[T any](slot Slot[T], fn driver func(buf *T))
func (s Scope) WaitTimeout(d Duration) bool
func (s Scope) Wait()
```

### 10.6. OS и компилаторни Capabilities (`FS`, `Env`, `Stdio`, `Exec` — `Scoped`)

Тези 4 capabilities позволяват на самохостващия компилатор `finc1` да работи изцяло в рамките на финитисткия модел:

```go
// FS — файлова система с явни, ограничени буфери
func (fs FS) ReadFileInto(path string, dst []byte) (usize, error)
func (fs FS) WriteFile(path string, data []byte) error
func (fs FS) IsDir(path string) bool
// Попълва сортирани имена на `.fc` файлове в dst; връща броя им
func (fs FS) ListSourceFiles(dir string, dst []string) (usize, error)
func (fs FS) MkdirTemp(prefix string) (string, error)
func (fs FS) RemoveAll(path string) error

// Env — аргументи от командния ред и променливи на средата
func (e Env) ArgsCount() usize
func (e Env) Arg(i usize) string
func (e Env) GetEnv(key string) (string, bool)

// Stdio — стандартен вход/изход за диагностики и CLI изход
func (io Stdio) WriteStdout(data []byte) error
func (io Stdio) WriteStderr(data []byte) error

// Exec — стартиране на външен инструмент (напр. `go build`), само в `driver`
type ExitStatus struct {
	Code int
}
driver func (ex Exec) Run(
	ctx Context,
	bin string,
	args []string,
	env []string,
	workdir string,
	stderrBuf []byte,
) (ExitStatus, usize, error)
```

### 10.7. Вградени чисти помощници за байтове и низове (`Total`)

За да може `compiler/` да манипулира байтове, имена и числа без неограничени низови конкатенации:

```go
func copy[T any](dst []T, src []T) usize
func bytesEqual(a []byte, b []byte) bool
func strBytes(s string) []byte          // read-only изглед към байтовете на s
func bytesToStr(b []byte) string        // копира крайния срез b в string
func formatUint(val u64, dst []byte) (usize, error) // форматира десетично в dst
func parseUint(src []byte) (u64, error)
```

---

## 11. Каталог на компилаторните грешки

Формат на диагностиката (идентичен в `finc0` и `finc1`):
`file.fc:line:col: error[E####]: <кратко описание>`

| Код | Име | Описание |
| :--- | :--- | :--- |
| `E1001` | `SYNTAX_ERROR` | Невалиден токен или нарушена граматика |
| `E1002` | `UNTERMINATED_LITERAL` | Незатворен низ, символ или коментар |
| `E2001` | `UNDEFINED_SYMBOL` | Недеклариран идентификатор |
| `E2002` | `TYPE_MISMATCH` | Несъвместими типове при присвояване, извикване или `return` |
| `E2003` | `ARITY_MISMATCH` | Грешен брой аргументи или връщани стойности |
| `E2004` | `INVALID_NIL` | Използване на `nil` за указател `*T` или `Scoped` ресурс |
| `E2005` | `UNINITIALIZED_SCOPED` | Декларация на `Scoped`/`Linear` променлива без инициализация |
| `E2006` | `IMPORT_CYCLE` | Цикличен импорт между локални пакети |
| `E2010` | `UNIVERSE_LEVEL_MISMATCH` | Опит за `Type : Type` или смесване на `Code n` с `Code m` ($n \neq m$) |
| `E2011` | `NON_PARAMETRIC_OP` | Опит за инспекция на типов параметър `[T any]` в чист слой |
| `E3001` | `GLOBAL_SCOPED_VAR` | Опит за глобална променлива от тип `Scoped` или указател `*T` |
| `E3002` | `SCOPE_ESCAPE` | Опит `Scoped` ресурс или `&local` да избяга в по-външен обхват или `return` |
| `E3003` | `USE_AFTER_MOVE` | Използване на `Linear` ресурс след прехвърляне (напр. след `conns.Go`) |
| `E3004` | `DISCARDED_LINEAR` | Изхвърляне на `Linear` ресурс с `_ = r` или игнориран резултат |
| `E3005` | `LINEAR_RESOURCE_LEAK` | Път в контролния граф, при който `Linear` ресурс не е затворен/освободен |
| `E4001` | `COMPTIME_OUT_OF_FUEL` | Изчерпан горивен лимит при оценка в Слой 1 (`comptime`) |
| `E4002` | `UNPROVEN_TERMINATION` | Цикъл `for` или директна рекурсия в `func` без доказана намаляваща мярка |
| `E4003` | `ARRAY_BOUNDS_COMPTIME` | Отрицателен или неконстантен размер на статичен масив `[N]T` |
| `E4004` | `MUTUAL_RECURSION_NO_MEASURE` | Взаимно рекурсивна група (SCC) в `func` без обща намаляваща мярка `decreases` |
| `E5001` | `DRIVER_CALL_IN_TOTAL` | Тотална `func` или `comptime func` извиква `driver func` или съдържа `driver for` |
| `E5002` | `INVALID_MAIN_SIGNATURE` | `main` има невалидна сигнатура или недопустими параметри |

---

## 12. Еталонна програма: TCP Echo сървър с Graceful Shutdown

Файлът `examples/tcp_echo.fc` трябва да се компилира без модификации и с `finc0` (Go), и с `finc1` / `finc2` (FinCore):

```go
package main

const (
	MaxConns     = 1024
	BufSize      = 4096
	IdleTimeout  = 30 * Second
	DrainHorizon = 5 * Second
)

// Слой 1 (comptime): интенсионален код на буферния пул -> сгъва се до [1024][4096]uint8
comptime func PoolCode(maxConns usize, bufSize usize) Code 0 {
	return CodeArray(maxConns, CodeArray(bufSize, CodeU8()))
}

type ConnPool = El(PoolCode(MaxConns, BufSize))

// Слой 2 (тотална функция): компилаторът доказва терминиране по Правило T2
func writeAll(ctx Context, conn Conn, data []byte) error {
	for rem := data; len(rem) > 0; {
		n, err := conn.Write(ctx, rem)
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrZeroWrite
		}
		rem = rem[n:]
	}
	return nil
}

// Слой 3 (неявна параметричност): `conn` е Linear (затваря се с defer),
// `buf` е scoped указател към слота в pool (не може да избяга извън handleConn)
driver func handleConn(drainCtx Context, hardCtx Context, conn Conn, buf *[BufSize]byte) {
	defer conn.Close()

	driver for {
		if drainCtx.Done() {
			_ = conn.CloseWrite()
			return
		}

		readCtx := hardCtx.WithTimeout(IdleTimeout)
		n, err := conn.Read(readCtx, buf[:])
		if err != nil {
			return
		}

		if err := writeAll(hardCtx, conn, buf[:n]); err != nil {
			return
		}
	}
}

driver func main(net Net, sig Signals) error {
	ln, err := net.ListenTCP("0.0.0.0:8080")
	if err != nil {
		return err
	}
	defer ln.Close()

	drainCtx, stopSignals := sig.NotifyContext(SIGINT, SIGTERM)
	defer stopSignals()

	var pool ConnPool

	hardCtx, cancelHard := NewContext()
	defer cancelHard()

	scope conns := NewScope(MaxConns) {
		driver for {
			slot, err := conns.AcquireSlot(drainCtx, &pool)
			if err != nil {
				break
			}

			conn, err := ln.Accept(drainCtx)
			if err != nil {
				conns.ReleaseSlot(slot)
				if drainCtx.Done() {
					break
				}
				continue
			}

			conns.Go(slot, driver func(buf *[BufSize]byte) {
				handleConn(drainCtx, hardCtx, conn, buf)
			})
		}

		_ = ln.Close()

		if !conns.WaitTimeout(DrainHorizon) {
			cancelHard()
			conns.Wait()
		}
	}

	return nil
}
```

---

## 13. Приемателни, конформансни и бутстрап тестове (Conformance Suite)

Имплементацията се счита за завършена едва когато **и `finc0`, и самокомпилираният `finc2`** преминават всички 4 групи тестове.

### 13.1. Задължителни негативни тестове (`testdata/fail/`)

1. `fail_type_in_type.fc` $\to$ `E2010`: `CodeUniv(0)` върнат от `comptime func() Code 0`.
2. `fail_comptime_loop.fc` $\to$ `E4001`: безкрайна `comptime` рекурсия удря `--comptime-fuel=1000`.
3. `fail_total_missing_zero_check.fc` $\to$ `E4002`: `writeAll` без `if n == 0`.
4. `fail_total_infinite_for.fc` $\to$ `E4002`: `for {}` вътре в `func`.
5. `fail_mutual_recursion_unbounded.fc` $\to$ `E4004`: две взаимно рекурсивни `func` функции без обща намаляваща мярка `decreases`.
6. `fail_driver_in_total.fc` $\to$ `E5001`: `func` извиква `driver func` или съдържа `driver for`.
7. `fail_scope_escape_var.fc` $\to$ `E3002`: записване на `buf` от `conns.Go` във външна променлива.
8. `fail_scope_escape_return.fc` $\to$ `E3002`: връщане на `&local` или на `conn` извън блока му.
9. `fail_global_conn.fc` $\to$ `E3001`: глобална променлива `var g Conn`.
10. `fail_nil_conn.fc` $\to$ `E2004`: опит `var c Conn = nil`.
11. `fail_use_after_move.fc` $\to$ `E3003`: използване на `conn` след прехвърляне в `conns.Go(...)`.
12. `fail_slot_leak.fc` $\to$ `E3005`: `AcquireSlot` без `ReleaseSlot` в клона за грешка на `Accept`.
13. `fail_conn_leak.fc` $\to$ `E3005`: `handleConn` без `defer conn.Close()`.

### 13.2. Задължителни позитивни тестове (`testdata/pass/`)

1. `pass_comptime_pool.fc`: `PoolCode` и `El(...)` генерират `[1024][4096]uint8`.
2. `pass_total_algorithms.fc`: бинарно търсене с `decreases(hi - lo)`, цикъл `for i := 0; i < len(a); i++`, `writeAll` и взаимна рекурсия с `decreases(depth)`.
3. `pass_parametric_id.fc`: `func id[T any](x T) T` и `func swap[A, B any](a A, b B) (B, A)`.
4. `pass_methods_and_arena.fc`: методи върху указателен receiver `func (a *Arena) Push(v u32) (u32, error)` и фиксирана хеш таблица.
5. `pass_defer_order.fc`: `defer` в ред `3 -> 2 -> 1` при нормален и ранен `return`.
6. `pass_tcp_echo.fc`: генерираният Go код минава `go vet` и `CGO_ENABLED=0 go build`.

### 13.3. End-to-End интеграционни тестове за TCP Echo (`testdata/integration/`)

Изпълняват се и със статичен бинарен файл (`CGO_ENABLED=0`), и с `--race`:
1. **Функционален ехо тест:** 50 паралелни клиента, потоци от 1 byte до 256 KB, побитова идентичност.
2. **Backpressure при `MaxConns = 4`:** 5-и клиент чака без краш и се обслужва веднага след освобождаване на слот.
3. **Clean Graceful Shutdown (`SIGTERM`):** активен кадър завършва + TCP FIN (`EOF`) + exit code `0` за $< 1\text{ s}$.
4. **Forced Shutdown след `DrainHorizon = 300ms`:** мълчалив клиент бива прекъснат чрез `cancelHard()` + `AfterFunc` точно след $\approx 300\text{ ms}$, exit code `0`.
5. **Нула течове:** `runtime.NumGoroutine()` стабилен в `fincrt` тестовете; `/proc/$PID/fd` на Linux стабилен след 200 кратки връзки.

### 13.4. Тестове за самохостване и бутстрап неподвижна точка (Bootstrap Gate)

Автоматизирани чрез `go run ./scripts/bootstrap_verify.go`:
1. `finc0 build --emit-go=stage1.go -o finc1 ./compiler` успява с exit code `0` и `go vet` на `stage1.go` е чист.
2. `finc1 build --emit-go=stage2.go -o finc2 ./compiler` успява с exit code `0`.
3. `finc2 build --emit-go=stage3.go -o finc3 ./compiler` успява с exit code `0`.
4. **Твърд критерий за неподвижна точка:** `sha256(stage2.go) == sha256(stage3.go)` (побитово идентичен генериран Go код!).
5. **Кръстосана идентичност на потребителски код:** `finc1` и `finc2` генерират побитово идентичен Go код за `examples/tcp_echo.fc`.
6. **Пълна регресия върху `finc2`:** `finc2` минава всички 13 негативни теста (`testdata/fail/`), всички 6 позитивни теста (`testdata/pass/`) и всички 5 TCP Echo интеграционни теста (`testdata/integration/`).

---

## 14. Поетапен план за имплементация (Milestones M0–M8)

Кодиращият агент имплементира проекта строго последователно. Преминаването към следващ етап е разрешено само след 100% зелени тестове на текущия етап.

### Фаза I — Stage 0 Bootstrap компилатор на Go (`bootstrap/` + `runtime/fincrt/`)

* **Milestone M0 (Lexer & Parser в Go):** `diag`, `lexer` (автоматично `;`), `ast`, `parser` (вкл. методи, `match`, `scope`, `decreases`).
  * *Готово:* `finc0 parse examples/tcp_echo.fc` + unit тестове за цялата EBNF граматика.
* **Milestone M1 (Resolver, Слой 1 `comptime` и Type Checker в Go):** многофайлови пакети, горивен `comptime` за `Code n`/`El`, Type Checker (`Value`, `Scoped`, `Linear`, методи, забрана на `nil` за ресурси).
  * *Готово:* минават `fail_type_in_type`, `fail_comptime_loop`, `fail_global_conn`, `fail_nil_conn`, `pass_comptime_pool`, `pass_methods_and_arena`.
* **Milestone M2 (Слой 3 Scope Checker и Слой 2 Totality Checker в Go):** правила S1–S5 и T1–T4 (вкл. Tarjan SCC за взаимна рекурсия с `decreases(depth)`).
  * *Готово:* минават всички 13 негативни теста в `testdata/fail/` и `finc0 check examples/tcp_echo.fc` връща `0`.
* **Milestone M3 (Go Runtime `runtime/fincrt`):** `Context`/`WithTimeout`, `Signals`, `Net`/`Listener`/`Conn` (`AfterFunc`), `Scope`/`Slot` (`idleCh`), OS capabilities (`FS`, `Env`, `Stdio`, `Exec`) и `builtins.go`.
  * *Готово:* `go test -race -count=5 ./runtime/fincrt/...` с 0 data races и 0 изтекли горутини.
* **Milestone M4 (IR Lowering и детерминистичен Go Codegen в Go):** сгъване на `El(...)`, `scope` бариери, Go generics, `enum`/`match`, `_fc_main` инжекция на capabilities, `go/format`.
  * *Готово:* всички тестове в `testdata/pass/` минават `go vet` и `CGO_ENABLED=0 go build`.
* **Milestone M5 (Пълна верификация на Stage 0 — `tcp_echo.fc`):**
  * *Готово:* `finc0 build` компилира `examples/tcp_echo.fc` и всички 5 интеграционни теста от Раздел 13.3 минават (и статично, и с `--race`).

### Фаза II — Stage 1+ Самохостващ компилатор на FinCore (`compiler/`)

* **Milestone M6 (Фундамент, Арени, Lexer и Parser на FinCore):**
  * Имплементация на `compiler/arena.fc`, `compiler/diag.fc`, `compiler/lexer.fc` (тотален с `decreases`) и `compiler/parser.fc` (взаимно рекурсивен тотален парсер с `decreases(depth)`).
  * *Готово:* `finc0 build -o finc1 ./compiler` компилира фронтенда на FinCore чисто; `finc1 parse examples/tcp_echo.fc` и `finc1 parse ./compiler` успяват без грешки.
* **Milestone M7 (Семантични фази и детерминистичен Go Codegen на FinCore):**
  * Имплементация на `compiler/comptime.fc`, `compiler/checker.fc` (Resolver + Type + Scope + Totality SCC) и `compiler/codegen.fc`, плюс `compiler/main.fc` (`FS`, `Env`, `Stdio`, `Exec`).
  * *Готово:* `finc0 build -o finc1 ./compiler` успява и `finc1` минава всички 13 негативни теста (`testdata/fail/`) и компилира `examples/tcp_echo.fc`.
* **Milestone M8 (Самокомпилация и Fixed-Point Bootstrap Gate):**
  * Имплементация и изпълнение на `scripts/bootstrap_verify.go`:
    1. `finc0` компилира `./compiler` $\to$ `finc1` (`stage1.go`);
    2. `finc1` компилира `./compiler` $\to$ `finc2` (`stage2.go`);
    3. `finc2` компилира `./compiler` $\to$ `finc3` (`stage3.go`);
    4. Проверка `sha256(stage2.go) == sha256(stage3.go)`;
    5. Пълен прогон на всички тестове (Раздели 13.1, 13.2 и 13.3) директно с `finc2`.
  * *Готово:* Самохостването е доказано с побитова неподвижна точка; `bootstrap/` може да бъде замразен като исторически Stage 0 артефакт.