// renderHeader is generating header for each page
async function renderHeader(containerSelector) {
    const headerContainer = document.querySelector(containerSelector);
    if (!headerContainer) return;

    function createAvatar(username) {
        const firstLetter = (username || '?').charAt(0).toUpperCase();
        const colors = [
            '#ef4444', '#f97316', '#eab308', '#84cc16', '#22c55e',
            '#14b8a6', '#06b6d4', '#3b82f6', '#8b5cf6', '#d946ef'
        ];
        const colorIndex = (firstLetter.charCodeAt(0) - 'A'.charCodeAt(0)) % colors.length;
        const color = colors[colorIndex];

        return `
            <div class="ui avatar image" style="background-color: ${color}; color: white; display: flex; align-items: center; justify-content: center; font-weight: bold;">
                ${firstLetter}
            </div>
        `;
    }

    let user = null;
    try {
        const response = await fetch('/api/user/profile');

        if (response.status === 401 || response.status === 403) {
            user = null;
        } else if (response.ok) {
            user = await response.json();
        }

    } catch (error) {
        console.error("Could not fetch user profile for header:", error);
        user = null;
    }

    // build the html
    const menuHTML = `
        <div class="ui container">
            <a class="header item" href="/repos/">
                <i class="box icon"></i>
                Conveyor
            </a>
            <a class="item" href="/repos/">Repositories</a>
            <a class="item" href="#">Docs</a>
            <a class="item" href="#">API</a>
            <div class="right menu" id="header-right-menu">
                <!-- will be injected here -->
            </div>
        </div>
    `;
    headerContainer.innerHTML = menuHTML;

    const rightMenu = document.getElementById('header-right-menu');
    if (user) {
        // User is logged in
        rightMenu.innerHTML = `
            <div class="ui simple dropdown item">
                ${createAvatar(user.username)}
                <i class="dropdown icon"></i>
                <div class="menu">
                    <div class="header">Signed in as <strong>${user.username}</strong></div>
                    <a class="item" href="/profile.html"><i class="user icon"></i> Profile</a>
                    <a class="item" href="/settings/index.html"><i class="cog icon"></i> Settings</a>
                    ${user.is_admin ? '<a class="item" href="/admin/index.html"><i class="shield alternate icon"></i> Admin</a>' : ''}
                    <div class="divider"></div>
                    <a class="item" href="/api/logout"><i class="sign out icon"></i> Sign Out</a>
                </div>
            </div>
        `;
        // Initialize the Fomantic UI dropdown
        $('.ui.dropdown').dropdown();
    } else {
        // User is not logged in
        rightMenu.innerHTML = `
            <a class="item" href="/login.html">Sign In</a>
            <a class="item" href="/register.html">Register</a>
        `;
    }
}