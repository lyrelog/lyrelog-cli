struct Cart {
    var items: [Int]
    @inline(__always) func total() -> Int {
        var sum = 0
        for i in items { sum += price(i) }
        return sum
    }
    @inline(never) func price(_ i: Int) -> Int {
        if i == 42 { fatalError("bad item") }
        return i * 3
    }
}
final class Checkout {
    @inline(never) func run(_ c: Cart) -> Int { c.total() + 1 }
}
@inline(never) func entry() {
    let c = Cart(items: Array(1...Int(CommandLine.arguments.count + 2)))
    print(Checkout().run(c))
}
entry()
