' Stops the background planner server.
'
' The launcher starts the server without a console window, so there is no
' window to close. Double-click this file when you actually want it to stop
' (it is safe to leave the server running, though - it is idle and tiny).
'
' ASCII-only on purpose: wscript reads .vbs as ANSI.
Option Explicit

Dim sh, result
Set sh = CreateObject("WScript.Shell")

result = sh.Run("taskkill /IM planner-server.exe /F", 0, True)

If result = 0 Then
  MsgBox "Time Planner has been stopped.", 64, "Time Planner"
Else
  MsgBox "Nothing to stop - the server is not running.", 48, "Time Planner"
End If
