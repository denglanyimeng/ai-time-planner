' ---------------------------------------------------------------
'  time-planner launcher  (double-click this, or use the desktop icon)
'
'  What it does:
'    1. checks whether http://localhost:8080 already answers
'    2. if not, starts data\planner-server.exe completely hidden
'       (window state 0 = no console window flashing on the taskbar)
'    3. opens the app in the default browser
'
'  Kept ASCII-only on purpose: wscript reads .vbs as ANSI, so any
'  Chinese literal in here would show up as garbage on some machines.
' ---------------------------------------------------------------
Option Explicit

Dim fso, sh, here, exePath, url, i, started

Set fso = CreateObject("Scripting.FileSystemObject")
Set sh  = CreateObject("WScript.Shell")

here    = fso.GetParentFolderName(WScript.ScriptFullName)
exePath = fso.BuildPath(here, "data\planner-server.exe")
url     = "http://localhost:8080/"

Function ServerUp(base)
  Dim http
  ServerUp = False
  On Error Resume Next
  Set http = CreateObject("MSXML2.ServerXMLHTTP.6.0")
  http.setTimeouts 800, 800, 800, 800
  http.open "GET", base & "api/health", False
  http.send
  If Err.Number = 0 Then
    If http.status = 200 Then ServerUp = True
  End If
  Err.Clear
  On Error GoTo 0
End Function

started = False

If Not ServerUp(url) Then
  If fso.FileExists(exePath) Then
    sh.CurrentDirectory = here
    sh.Run """" & exePath & """", 0, False
    started = True
    For i = 1 To 40
      WScript.Sleep 250
      If ServerUp(url) Then Exit For
    Next
  Else
    MsgBox "planner-server.exe was not found in:" & vbCrLf & _
           exePath & vbCrLf & vbCrLf & _
           "Run the build once (go build -o data\planner-server.exe ./cmd/server) " & _
           "and then use this launcher again.", 48, "Time Planner"
  End If
End If

sh.Run url, 1, False
