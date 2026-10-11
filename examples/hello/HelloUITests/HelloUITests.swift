import XCTest

final class HelloUITests: XCTestCase {
    func testButtonChangesLabel() {
        let app = XCUIApplication()
        app.launch()

        let status = app.staticTexts["statusLabel"]
        XCTAssertTrue(status.waitForExistence(timeout: 10))
        XCTAssertEqual(status.value as? String, "Ready")

        let button = app.buttons["Change label"]
        XCTAssertTrue(button.exists)
        button.click()
        XCTAssertEqual(status.value as? String, "Changed")
    }
}
